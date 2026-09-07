// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, it, expect, vi } from "vitest";
import type { DeviceAuthClient } from "@workos/device-auth";
import type { WorkOSClients } from "@workos/agent-sdk";
import { PushSettings } from "./PushSettings.js";
import { Code, ConnectError } from "@connectrpc/connect";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const preferences = {
  revision: 0n,
  quietEnabled: false,
  quietStartUtc: "22:00",
  quietEndUtc: "07:00",
};
describe("PushSettings", () => {
  it("keeps unavailable browser delivery separate from editable quiet hours", async () => {
    const setPushPreferences = vi
      .fn()
      .mockResolvedValue({ preferences: { ...preferences, quietEnabled: true } });
    const clients = {
      notifications: {
        getPushPreferences: vi.fn().mockResolvedValue({
          preferences,
          webPushPublicKey: "",
          webPushUnavailableReason: "Web Push is not configured on this WorkOS host.",
        }),
        setPushPreferences,
      },
    } as unknown as WorkOSClients;
    render(<PushSettings workosClients={clients} />);
    await screen.findByText("Quiet hours");
    expect(
      screen.getByRole("button", { name: "Enable browser alerts" }).hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: "Quiet hours" }));
    fireEvent.click(screen.getByRole("button", { name: "Save quiet hours" }));
    await waitFor(() => {
      expect(setPushPreferences).toHaveBeenCalledWith({
        preferences: { ...preferences, quietEnabled: true },
        expectedRevision: 0n,
      });
    });
    await screen.findByText("Quiet hours saved.");
  });
  it("reloads the current revision after another device changes preferences", async () => {
    const latest = { ...preferences, revision: 2n, quietStartUtc: "21:00" };
    const getPushPreferences = vi
      .fn()
      .mockResolvedValueOnce({
        preferences,
        webPushPublicKey: "",
        webPushUnavailableReason: "unavailable",
      })
      .mockResolvedValue({
        preferences: latest,
        webPushPublicKey: "",
        webPushUnavailableReason: "unavailable",
      });
    const setPushPreferences = vi
      .fn()
      .mockRejectedValue(new ConnectError("private details", Code.Aborted));
    render(
      <PushSettings
        workosClients={
          { notifications: { getPushPreferences, setPushPreferences } } as unknown as WorkOSClients
        }
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Save quiet hours" }));
    await screen.findByText(
      "Quiet hours changed on another device. The latest settings are loaded.",
    );
    expect(screen.getByLabelText("From (UTC)").getAttribute("value")).toBe("21:00");
    expect(setPushPreferences).toHaveBeenCalledTimes(1);
  });
  it("keeps failed preference loads retryable and hides transport details", async () => {
    const getPushPreferences = vi
      .fn()
      .mockRejectedValueOnce(new Error("private details"))
      .mockResolvedValue({
        preferences,
        webPushPublicKey: "",
        webPushUnavailableReason: "unavailable",
      });
    render(
      <PushSettings
        workosClients={{ notifications: { getPushPreferences } } as unknown as WorkOSClients}
      />,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    await screen.findByText("Quiet hours");
    expect(screen.queryByText("private details")).toBeNull();
    expect(getPushPreferences).toHaveBeenCalledTimes(2);
  });
});

describe("browser subscription reconciliation", () => {
  const key = "BA" + "A".repeat(85);
  const currentKey = Uint8Array.from(atob(key), (c) => c.charCodeAt(0)).buffer;
  function browser(existingKey: ArrayBuffer = currentKey) {
    const subscription = {
      endpoint: "https://push.fixture.test/subscription",
      options: { applicationServerKey: existingKey },
      toJSON: () => ({ keys: { p256dh: "fixture-public-key", auth: "fixture-auth" } }),
      unsubscribe: vi.fn().mockResolvedValue(true),
    };
    const manager = {
      getSubscription: vi.fn().mockResolvedValue(subscription),
      subscribe: vi.fn().mockResolvedValue(subscription),
    };
    const worker = { pushManager: manager };
    vi.stubGlobal("Notification", { requestPermission: vi.fn().mockResolvedValue("granted") });
    vi.stubGlobal("PushManager", function PushManager() {});
    vi.stubGlobal("navigator", {
      serviceWorker: {
        getRegistration: vi.fn().mockResolvedValue(worker),
        register: vi.fn().mockResolvedValue(worker),
        ready: Promise.resolve(worker),
      },
    });
    vi.stubGlobal("crypto", {
      subtle: { digest: vi.fn().mockResolvedValue(new Uint8Array(32).fill(1).buffer) },
    });
    return { subscription, manager };
  }
  function mount(digest = "") {
    const subscribePush = vi.fn().mockResolvedValue({});
    const unsubscribePush = vi.fn().mockResolvedValue({});
    const getPushPreferences = vi.fn().mockResolvedValue({
      preferences,
      webPushPublicKey: key,
      webPushUnavailableReason: "",
      webPushSubscriptionDigest: digest,
    });
    const deviceAuth = { getCurrentDevice: vi.fn().mockResolvedValue({ deviceId: "device-1" }) };
    render(
      <PushSettings
        workosClients={
          {
            notifications: { getPushPreferences, subscribePush, unsubscribePush },
          } as unknown as WorkOSClients
        }
        deviceAuth={deviceAuth as unknown as DeviceAuthClient}
      />,
    );
    return { subscribePush, unsubscribePush };
  }
  it("requires a click to reconnect a subscription revoked on Core", async () => {
    const { manager } = browser();
    const { subscribePush } = mount();
    const button = await screen.findByRole("button", { name: "Reconnect browser alerts" });
    expect(subscribePush).not.toHaveBeenCalled();
    fireEvent.click(button);
    await screen.findByText("Background alerts enabled for this browser.");
    expect(manager.subscribe).not.toHaveBeenCalled();
    expect(subscribePush).toHaveBeenCalledOnce();
  });
  it("replaces subscriptions when the VAPID key changes", async () => {
    const { subscription, manager } = browser(new Uint8Array([4, 7, 8]).buffer);
    manager.subscribe.mockResolvedValue({
      ...subscription,
      options: { applicationServerKey: currentKey },
    });
    manager.getSubscription
      .mockResolvedValueOnce(subscription)
      .mockResolvedValueOnce(subscription)
      .mockResolvedValue(null);
    const { unsubscribePush, subscribePush } = mount(`sha256:${"01".repeat(32)}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reconnect browser alerts" }));
    await screen.findByText("Background alerts enabled for this browser.");
    expect(unsubscribePush).toHaveBeenCalledOnce();
    expect(subscription.unsubscribe).toHaveBeenCalledOnce();
    expect(manager.subscribe).toHaveBeenCalledOnce();
    expect(subscribePush).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Reconnect browser alerts" })).toBeNull();
  });
  it("removes a newly created browser subscription when Core registration fails", async () => {
    const { subscription, manager } = browser();
    manager.getSubscription.mockResolvedValue(null);
    const { subscribePush } = mount();
    subscribePush.mockRejectedValue(new Error("private details"));
    const button = await screen.findByRole("button", { name: "Enable browser alerts" });
    await waitFor(() => {
      expect(button.hasAttribute("disabled")).toBe(false);
    });
    fireEvent.click(button);
    await screen.findByText(/Could not save notification preferences/);
    expect(subscription.unsubscribe).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Disable browser alerts" })).toBeNull();
    expect(screen.queryByText("Background alerts enabled for this browser.")).toBeNull();
  });
  it("does not claim browser cleanup succeeded when unsubscribe returns false", async () => {
    const { subscription } = browser();
    vi.mocked(subscription.unsubscribe).mockResolvedValue(false);
    const { unsubscribePush } = mount(`sha256:${"01".repeat(32)}`);
    const button = await screen.findByRole("button", { name: "Disable browser alerts" });
    await waitFor(() => {
      expect(button.hasAttribute("disabled")).toBe(false);
    });
    fireEvent.click(button);
    await screen.findByText(/Browser cleanup failed/);
    expect(unsubscribePush).toHaveBeenCalledOnce();
    expect(screen.queryByText("Background alerts disabled for this browser.")).toBeNull();
    expect(screen.getByRole("button", { name: "Disable browser alerts" })).toBeTruthy();
  });
  it("preserves browser state when Core cannot revoke delivery", async () => {
    const { subscription } = browser();
    const { unsubscribePush } = mount(`sha256:${"01".repeat(32)}`);
    unsubscribePush.mockRejectedValue(new Error("private details"));
    const button = await screen.findByRole("button", { name: "Disable browser alerts" });
    await waitFor(() => {
      expect(button.hasAttribute("disabled")).toBe(false);
    });
    fireEvent.click(button);
    await screen.findByText(/Could not save notification preferences/);
    expect(subscription.unsubscribe).not.toHaveBeenCalled();
  });
  it("keeps quiet hours editable when browser subscription lookup fails", async () => {
    const { manager } = browser();
    manager.getSubscription.mockRejectedValue(new Error("browser failure"));
    mount();
    await screen.findByText(/Browser subscription could not be checked/);
    expect(screen.getByRole("button", { name: "Save quiet hours" }).hasAttribute("disabled")).toBe(
      false,
    );
  });
});
