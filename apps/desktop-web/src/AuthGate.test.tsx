// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DeviceAuthClient } from "@workos/device-auth";
import { afterEach, expect, it, vi } from "vitest";
import { AuthGate } from "./AuthGate.js";
import { clearLocalDesktopState } from "./desktopLocalState.js";
import {
  patchSessionContinuity,
  readSessionContinuity,
  sessionContinuityKey,
} from "./sessionContinuity.js";

afterEach(async () => {
  cleanup();
  await clearLocalDesktopState();
});

it("retains drafts on failed Forget and clears local content only after success", async () => {
  const key = sessionContinuityKey("owner", "project", "session");
  await patchSessionContinuity(key, {
    draft: "Fixture private draft",
    addPending: [{ clientInputId: "receipt", text: "Pending fixture", phase: "recovering" }],
  });
  localStorage.setItem("workos.desktop-projection.v1", "fixture references");
  sessionStorage.setItem("workos.activeProjectId", "fixture project");
  const forget = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(undefined);
  const denied = () => Promise.reject(new ConnectError("unpaired", Code.Unauthenticated));
  const auth = {
    getAuthMode: vi.fn().mockResolvedValue("pairing"),
    restoreSession: denied,
    reauthenticate: denied,
    forget,
  } as unknown as DeviceAuthClient;
  render(<AuthGate deviceAuth={auth}>Private desktop</AuthGate>);
  await userEvent.click(await screen.findByRole("button", { name: "Forget this browser" }));
  await screen.findByText("This browser could not be fully cleared. Try Forget again.");
  expect((await readSessionContinuity(key)).draft).toBe("Fixture private draft");
  await userEvent.click(screen.getByRole("button", { name: "Forget this browser" }));
  await screen.findByText("This browser is not yet paired with this WorkOS.");
  expect((await readSessionContinuity(key)).draft).toBe("");
  expect((await readSessionContinuity(key)).pending).toEqual([]);
  expect(localStorage.getItem("workos.desktop-projection.v1")).toBeNull();
  expect(sessionStorage.getItem("workos.activeProjectId")).toBeNull();
  expect(screen.queryByText("Private desktop")).toBeNull();
});

it("requires the password again after an expired session without proving a stored device key", async () => {
  const reauthenticate = vi.fn();
  const loginWithPassword = vi.fn().mockResolvedValue({ deviceId: "fixture-device" });
  const auth = {
    getAuthMode: vi.fn().mockResolvedValue("password"),
    restoreSession: vi.fn().mockResolvedValue(undefined),
    reauthenticate,
    loginWithPassword,
  } as unknown as DeviceAuthClient;
  render(<AuthGate deviceAuth={auth}>Private desktop</AuthGate>);
  const form = await screen.findByTestId("password-login");
  expect(form).toBeTruthy();
  expect(reauthenticate).not.toHaveBeenCalled();
  await userEvent.type(screen.getByRole("textbox", { name: "Username" }), "owner");
  await userEvent.type(screen.getByLabelText("Password"), "fixture-secret");
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByText("Private desktop");
  expect(loginWithPassword).toHaveBeenCalledWith({
    username: "owner",
    password: "fixture-secret",
    deviceName: "Desktop browser",
    deviceClass: "desktop",
  });
  expect(reauthenticate).not.toHaveBeenCalled();
});

it("does not expose pairing UI or mount Desktop after a failed password", async () => {
  const reauthenticate = vi.fn();
  const auth = {
    getAuthMode: vi.fn().mockResolvedValue("password"),
    restoreSession: vi.fn().mockResolvedValue(undefined),
    reauthenticate,
    loginWithPassword: vi.fn().mockRejectedValue(new ConnectError("wrong", Code.Unauthenticated)),
  } as unknown as DeviceAuthClient;
  render(<AuthGate deviceAuth={auth}>Private desktop</AuthGate>);
  await screen.findByTestId("password-login");
  await userEvent.type(screen.getByRole("textbox", { name: "Username" }), "owner");
  await userEvent.type(screen.getByLabelText("Password"), "wrong-fixture");
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByText(/Sign in failed/);
  await waitFor(() => {
    expect(screen.getByLabelText<HTMLInputElement>("Password").value).toBe("");
  });
  expect(screen.queryByText("Private desktop")).toBeNull();
  expect(screen.queryByText(/pairing QR code/)).toBeNull();
  expect(reauthenticate).not.toHaveBeenCalled();
});
