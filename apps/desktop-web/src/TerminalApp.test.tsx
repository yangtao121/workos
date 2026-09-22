// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import { afterEach, expect, it, vi } from "vitest";
import { TerminalApp } from "./TerminalApp.js";

afterEach(cleanup);
function fixture() {
  const attach = vi.fn(() =>
    Promise.resolve({
      session: { id: "exact" },
      attachment: { controls: false, controlGeneration: 2n },
    }),
  );
  const read = vi.fn<
    (request: {
      sessionId: string;
      after: bigint;
      maxBytes: number;
    }) => Promise<{ output: Uint8Array; cursor: bigint; closed: boolean }>
  >(() =>
    Promise.resolve({
      output: new TextEncoder().encode("same shell\n"),
      cursor: 11n,
      closed: false,
    }),
  );
  const detach = vi.fn(() => Promise.resolve({}));
  const create = vi.fn(),
    list = vi.fn();
  return {
    attach,
    read,
    detach,
    create,
    list,
    clients: {
      surfaceContinuity: { attachSurface: attach, listProjectSurfaces: list },
      ptySessions: { readPtySession: read, detachPtySession: detach, createPtySession: create },
    } as unknown as WorkOSClients,
  };
}
it("restores only the exact workload as observer and detaches on close", async () => {
  const f = fixture();
  const view = render(
    <TerminalApp
      workosClients={f.clients}
      activeProjectId="project"
      workloadId="exact"
      expectedWorkloadGeneration={7n}
    />,
  );
  await waitFor(() => {
    expect(f.attach).toHaveBeenCalledWith(
      expect.objectContaining({ workloadId: "exact", expectedWorkloadGeneration: 7n }),
    );
  });
  expect(f.list).not.toHaveBeenCalled();
  expect(f.create).not.toHaveBeenCalled();
  expect(await screen.findByText("observer")).toBeTruthy();
  view.unmount();
  await waitFor(() => {
    expect(f.detach).toHaveBeenCalledWith({ sessionId: "exact" });
  });
});
it("a vanished exact workload never falls back to creating another shell", async () => {
  const f = fixture();
  f.attach.mockRejectedValue(new ConnectError("stopped", Code.NotFound));
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText(/stopped or no longer accessible/)).toBeTruthy();
  expect(f.create).not.toHaveBeenCalled();
  expect(f.list).not.toHaveBeenCalled();
});
it("a transient output disconnect retries the same cursor without claiming the program stopped", async () => {
  const f = fixture();
  f.read.mockRejectedValueOnce(new ConnectError("offline", Code.Unavailable));
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  await waitFor(() => {
    expect(f.read).toHaveBeenCalledTimes(2);
  });
  expect(screen.getByText("observer")).toBeTruthy();
  expect(
    f.read.mock.calls.every(
      ([request]) => (request as unknown as { sessionId: string }).sessionId === "exact",
    ),
  ).toBe(true);
  expect(f.create).not.toHaveBeenCalled();
});
