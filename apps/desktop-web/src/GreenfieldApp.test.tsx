// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldApp } from "./GreenfieldApp.js";

afterEach(() => cleanup());

function clients(open: ReturnType<typeof vi.fn>): WorkOSClients {
  return { nativeSessions: { openGreenfieldDisplay: open } } as unknown as WorkOSClients;
}

describe("GreenfieldApp", () => {
  it("shows attached only when the runtime returns a connection", async () => {
    render(
      <GreenfieldApp
        clients={clients(
          vi.fn(() =>
            Promise.resolve({
              websocketPath: "/native/greenfield/session/code",
              compositorSessionId: "workos",
            }),
          ),
        )}
        sessionId="018f1a00-0000-7000-8000-000000000001"
      />,
    );
    await waitFor(() =>
      expect(screen.getByTestId("greenfield-status").getAttribute("data-status")).toBe("attached"),
    );
  });

  it("shows unavailable when the runtime rejects the display", async () => {
    render(
      <GreenfieldApp
        clients={clients(vi.fn(() => Promise.reject(new Error("unavailable"))))}
        sessionId="018f1a00-0000-7000-8000-000000000001"
      />,
    );
    await waitFor(() =>
      expect(screen.getByTestId("greenfield-status").getAttribute("data-status")).toBe("unavailable"),
    );
  });
});
