// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, it, expect, vi } from "vitest";
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
