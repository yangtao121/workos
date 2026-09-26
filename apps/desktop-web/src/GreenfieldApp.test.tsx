// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { GreenfieldApp } from "./GreenfieldApp.js";

const attach = vi.hoisted(() => vi.fn());
vi.mock("./greenfieldCompositor.js", () => ({
  MAX_GREENFIELD_CLIPBOARD_BYTES: 1024 * 1024,
  attachGreenfieldCompositor: attach,
}));

const bridge = {
  copyFromApp: vi.fn(),
  pasteIntoApp: vi.fn(),
  commitComposition: vi.fn(),
  setControlling: vi.fn(),
  dispose: vi.fn(),
};
const oldClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
beforeEach(() => {
  vi.clearAllMocks();
  attach.mockImplementation(
    (input: { options: { onConnectionStateChange?: (state: "open") => void } }) => {
      input.options.onConnectionStateChange?.("open");
      return Promise.resolve(bridge);
    },
  );
});
afterEach(() => {
  cleanup();
  if (oldClipboard) Object.defineProperty(navigator, "clipboard", oldClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
});

function clients(open: ReturnType<typeof vi.fn>): WorkOSClients {
  return { nativeSessions: { openGreenfieldDisplay: open } } as unknown as WorkOSClients;
}
function openDisplay(clipboardMaxBytes = 1024 * 1024) {
  return vi.fn(() =>
    Promise.resolve({
      websocketPath: "/native/greenfield/session/code",
      compositorSessionId: "workos",
      width: 1440,
      height: 900,
      clipboardMaxBytes,
    }),
  );
}

describe("GreenfieldApp", () => {
  it("passes the control generation and shows a connected signaling channel", async () => {
    const open = openDisplay();
    render(
      <GreenfieldApp
        clients={clients(open)}
        sessionId="018f1a00-0000-7000-8000-000000000001"
        controlGeneration={7n}
      />,
    );
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("connected");
    });
    expect(open).toHaveBeenCalledWith(
      expect.objectContaining({ controlGeneration: 7n, devicePixelRatioMillis: 1000 }),
    );
    expect(screen.getByText("显示通道已连接")).toBeTruthy();
  });

  it("does not attach an observer and makes input unavailable", () => {
    const open = openDisplay();
    render(<GreenfieldApp clients={clients(open)} sessionId="session" controls={false} />);
    expect(open).not.toHaveBeenCalled();
    expect(screen.getByTestId("greenfield-status").getAttribute("data-status")).toBe("observer");
    expect(screen.getByRole("button", { name: "粘贴到应用" }).hasAttribute("disabled")).toBe(true);
  });

  it("disables clipboard actions after signaling disconnects", async () => {
    render(<GreenfieldApp clients={clients(openDisplay())} sessionId="session" />);
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("connected");
    });
    const input = attach.mock.calls[0]?.[0] as {
      options: { onConnectionStateChange: (state: "closed") => void };
    };
    act(() => {
      input.options.onConnectionStateChange("closed");
    });
    expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("unavailable");
    expect(screen.getByRole("button", { name: "粘贴到应用" }).hasAttribute("disabled")).toBe(true);
  });

  it("copies actual app selection to the browser and reports write denial", async () => {
    const open = openDisplay();
    const writeText = vi.fn(() => Promise.reject(new Error("denied")));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    bridge.copyFromApp.mockResolvedValue("中文\t🙂\nline 2");
    render(<GreenfieldApp clients={clients(open)} sessionId="session" />);
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("connected");
    });
    await userEvent.click(screen.getByRole("button", { name: "复制到本机" }));
    expect(writeText).toHaveBeenCalledWith("中文\t🙂\nline 2");
    expect(screen.getByText(/复制失败：应用没有提供文本选区/)).toBeTruthy();
  });

  it("uses the Runtime clipboard limit for paste and composition feedback", async () => {
    const open = openDisplay(256 * 1024);
    const readText = vi.fn(() => Promise.resolve("x".repeat(256 * 1024 + 1)));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { readText } });
    render(<GreenfieldApp clients={clients(open)} sessionId="session" />);
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("connected");
    });
    await userEvent.click(screen.getByRole("button", { name: "粘贴到应用" }));
    expect(readText).toHaveBeenCalledOnce();
    expect(bridge.pasteIntoApp).not.toHaveBeenCalled();
    expect(screen.getByText("粘贴失败：文本超过 256 KiB")).toBeTruthy();
    const input = attach.mock.calls[0]?.[0] as {
      options: { maxClipboardBytes: number; onCompositionCommitted: (result: "too_large") => void };
    };
    expect(input.options.maxClipboardBytes).toBe(256 * 1024);
    act(() => {
      input.options.onCompositionCommitted("too_large");
    });
    expect(screen.getByText("输入失败：文本超过 256 KiB")).toBeTruthy();
  });

  it("rejects app selection above the Runtime clipboard limit before browser write", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    bridge.copyFromApp.mockResolvedValue("x".repeat(256 * 1024 + 1));
    render(<GreenfieldApp clients={clients(openDisplay(256 * 1024))} sessionId="session" />);
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-canvas")).toBe("connected");
    });
    await userEvent.click(screen.getByRole("button", { name: "复制到本机" }));
    expect(writeText).not.toHaveBeenCalled();
    expect(screen.getByText("复制失败：文本超过 256 KiB")).toBeTruthy();
  });

  it("shows unavailable when the runtime rejects the display", async () => {
    render(
      <GreenfieldApp
        clients={clients(vi.fn(() => Promise.reject(new Error("unavailable"))))}
        sessionId="018f1a00-0000-7000-8000-000000000001"
      />,
    );
    await waitFor(() => {
      expect(screen.getByTestId("greenfield-status").getAttribute("data-status")).toBe(
        "unavailable",
      );
    });
  });
});
