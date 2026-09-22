// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen } from "@testing-library/react";
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
