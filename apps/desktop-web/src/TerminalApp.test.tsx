// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { WorkOSClients } from "@workos/agent-sdk";
import { afterEach, expect, it, vi } from "vitest";
import { TerminalApp } from "./TerminalApp.js";

const oldClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
afterEach(() => {
  cleanup();
  window.getSelection()?.removeAllRanges();
  if (oldClipboard) Object.defineProperty(navigator, "clipboard", oldClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
});
function fixture(controls = false) {
  const attach = vi.fn(() =>
    Promise.resolve({
      session: { id: "exact", workloadGeneration: 7n },
      attachment: {
        id: "attachment",
        controls,
        controlGeneration: 2n,
        controlExpiresAt: { seconds: 2_000_000_000n, nanos: 0 },
      },
    }),
  );
  const read = vi.fn<
    (request: {
      sessionId: string;
      after: bigint;
      maxBytes: number;
    }) => Promise<{ output: Uint8Array; cursor: bigint; closed: boolean }>
  >((request) =>
    Promise.resolve({
      output: request.after === 0n ? new TextEncoder().encode("same shell\n") : new Uint8Array(),
      cursor: 11n,
      closed: false,
    }),
  );
  const detach = vi.fn(() => Promise.resolve({}));
  const write = vi.fn<
    (request: {
      sessionId: string;
      input: Uint8Array;
      controlGeneration: bigint;
    }) => Promise<Record<string, never>>
  >(() => Promise.resolve({}));
  const renew = vi.fn(() => Promise.resolve({}));
  const create = vi.fn(),
    list = vi.fn();
  return {
    attach,
    read,
    detach,
    write,
    renew,
    create,
    list,
    clients: {
      surfaceContinuity: {
        attachSurface: attach,
        listProjectSurfaces: list,
        renewSurfaceControl: renew,
      },
      ptySessions: {
        readPtySession: read,
        detachPtySession: detach,
        createPtySession: create,
        writePtySession: write,
      },
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

it("Ctrl+C copies a selection locally and sends ETX only when the selection is empty", async () => {
  const f = fixture(true);
  const writeText = vi.fn(() => Promise.resolve());
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  const output = screen.getByTestId("terminal-output");
  await waitFor(() => {
    expect(output.textContent).toBe("same shell\n");
  });
  const range = document.createRange();
  range.selectNodeContents(output);
  window.getSelection()?.addRange(range);
  fireEvent.keyDown(output, { key: "c", ctrlKey: true });
  await waitFor(() => {
    expect(writeText).toHaveBeenCalledWith("same shell\n");
  });
  expect(f.write).not.toHaveBeenCalled();
  window.getSelection()?.removeAllRanges();
  fireEvent.keyDown(output, { key: "c", ctrlKey: true });
  await waitFor(() => {
    expect(f.write).toHaveBeenCalledTimes(1);
  });
  const write = f.write.mock.calls[0]?.[0];
  expect(write?.sessionId).toBe("exact");
  expect(Array.from(write?.input ?? [])).toEqual([3]);
  expect(write?.controlGeneration).toBe(2n);
});

it("a failed clipboard write stops later chunks and reports uncertainty", async () => {
  const f = fixture(true);
  const pasted = "中文🙂\tline\n".repeat(3500);
  const readText = vi.fn(() => Promise.resolve(pasted));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { readText } });
  f.write
    .mockResolvedValueOnce({})
    .mockRejectedValueOnce(new ConnectError("offline", Code.Unavailable));
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  fireEvent.keyDown(screen.getByTestId("terminal-output"), { key: "v", ctrlKey: true });
  await waitFor(() => {
    expect(screen.getByTestId("terminal-clipboard-result").textContent).toContain(
      "Paste was not confirmed",
    );
  });
  expect(f.write).toHaveBeenCalledTimes(2);
  expect(f.write.mock.calls.every(([request]) => request.input.length <= 16 * 1024)).toBe(true);
});

it("reserves Ctrl+V's queue position while Chromium reads the clipboard", async () => {
  const f = fixture(true);
  let resolveRead: (text: string) => void = () => undefined;
  const readText = vi.fn(() => new Promise<string>((resolve) => (resolveRead = resolve)));
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { readText } });
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  const output = screen.getByTestId("terminal-output");
  fireEvent.keyDown(output, { key: "v", ctrlKey: true });
  fireEvent.keyDown(output, { key: "z" });
  expect(f.write).not.toHaveBeenCalled();
  resolveRead("中文🙂\tline\n");
  await waitFor(() => {
    expect(f.write).toHaveBeenCalledTimes(2);
  });
  expect(new TextDecoder().decode(f.write.mock.calls[0]?.[0]?.input)).toBe("中文🙂\tline\n");
  expect(new TextDecoder().decode(f.write.mock.calls[1]?.[0]?.input)).toBe("z");
});

it("reports browser clipboard refusal without writing to the PTY", async () => {
  const f = fixture(true);
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { readText: vi.fn(() => Promise.reject(new Error("denied"))) },
  });
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  fireEvent.keyDown(screen.getByTestId("terminal-output"), { key: "v", ctrlKey: true });
  await waitFor(() => {
    expect(screen.getByTestId("terminal-clipboard-result").textContent).toContain(
      "browser denied clipboard access",
    );
  });
  expect(f.write).not.toHaveBeenCalled();
});

it("a denied write revokes local control and prevents further input", async () => {
  const f = fixture(true);
  f.write.mockRejectedValueOnce(new ConnectError("taken over", Code.PermissionDenied));
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  const output = screen.getByTestId("terminal-output");
  fireEvent.keyDown(output, { key: "x" });
  expect(await screen.findByText("observer")).toBeTruthy();
  fireEvent.keyDown(output, { key: "y" });
  expect(f.write).toHaveBeenCalledTimes(1);
  expect(f.renew).not.toHaveBeenCalled();
});

it("an expired control lease switches to observer before any queued input reaches Runtime", async () => {
  const f = fixture(true);
  render(<TerminalApp workosClients={f.clients} activeProjectId="project" workloadId="exact" />);
  expect(await screen.findByText("controlling")).toBeTruthy();
  const now = vi.spyOn(Date, "now").mockReturnValue(2_000_000_001_000);
  try {
    fireEvent.keyDown(screen.getByTestId("terminal-output"), { key: "z" });
    expect(await screen.findByText("observer")).toBeTruthy();
    expect(f.write).not.toHaveBeenCalled();
  } finally {
    now.mockRestore();
  }
});
