// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { NativeSessionLease } from "./nativeSession.js";
import { NativeApp } from "./NativeApp.js";

class Peer {
  static instances: Peer[] = [];
  iceGatheringState = "complete";
  localDescription = { sdp: "offer" };
  ondatachannel?: (event: { channel: unknown }) => void;
  addTransceiver = vi.fn();
  createDataChannel = vi.fn();
  createOffer = vi.fn(() => Promise.resolve({ type: "offer", sdp: "offer" }));
  setLocalDescription = vi.fn(() => Promise.resolve());
  setRemoteDescription = vi.fn(() => Promise.resolve());
  close = vi.fn();
  constructor() {
    Peer.instances.push(this);
  }
}
function fixture(create = vi.fn(() => Promise.resolve({ session: { id: "session-1" } }))) {
  const nativeSessions = {
    createNativeSession: create,
    connectNativeSession: vi.fn(() => Promise.resolve({ answerSdp: "answer" })),
    closeNativeSession: vi.fn(() => Promise.resolve({})),
  };
  vi.stubGlobal("RTCPeerConnection", Peer);
  return { nativeSessions, clients: { nativeSessions } as unknown as WorkOSClients };
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Peer.instances = [];
});

describe("Native window lifecycle and input", () => {
  it("negotiates SCTP and closes the peer and session on unmount", async () => {
    const f = fixture();
    const view = render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalled();
    });
    const peer = firstPeer();
    expect(peer.createDataChannel).toHaveBeenCalled();
    view.unmount();
    expect(peer.close).toHaveBeenCalledOnce();
    await waitFor(() => {
      expect(f.nativeSessions.closeNativeSession).toHaveBeenCalledWith({ sessionId: "session-1" });
    });
  });
  it("closes a creation response arriving after the window disappeared", async () => {
    let complete!: (value: { session: { id: string } }) => void;
    const f = fixture(
      vi.fn(
        () =>
          new Promise((resolve) => {
            complete = resolve;
          }),
      ),
    );
    const view = render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    view.unmount();
    complete({ session: { id: "late-session" } });
    await waitFor(() => {
      expect(f.nativeSessions.closeNativeSession).toHaveBeenCalledWith({
        sessionId: "late-session",
      });
    });
    expect(Peer.instances).toHaveLength(0);
  });
  it("keeps one session across a responsive remount and closes it with the window", async () => {
    const f = fixture();
    const lease = new NativeSessionLease(true);
    const view = render(
      <NativeApp
        key="desktop"
        sessionLease={lease}
        workosClients={f.clients}
        activeProjectId="project"
      />,
    );
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalledTimes(1);
    });
    view.rerender(
      <NativeApp
        key="phone"
        sessionLease={lease}
        workosClients={f.clients}
        activeProjectId="project"
      />,
    );
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalledTimes(2);
    });
    expect(f.nativeSessions.createNativeSession).toHaveBeenCalledOnce();
    expect(f.nativeSessions.closeNativeSession).not.toHaveBeenCalled();
    view.unmount();
    lease.dispose();
    await waitFor(() => {
      expect(f.nativeSessions.closeNativeSession).toHaveBeenCalledOnce();
    });
  });
  it("releases a captured pointer outside the video and preserves right-button mapping", async () => {
    const f = fixture();
    render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalled();
    });
    const send = vi.fn();
    firstPeer().ondatachannel?.({
      channel: { label: "workos.input", readyState: "open", send, close: vi.fn() },
    });
    const video = screen.getByTestId("native-video");
    Object.defineProperties(video, { videoWidth: { value: 800 }, videoHeight: { value: 600 } });
    vi.spyOn(video, "getBoundingClientRect").mockReturnValue({
      x: 0,
      y: 0,
      left: 0,
      top: 0,
      width: 800,
      height: 600,
      right: 800,
      bottom: 600,
      toJSON: () => ({}),
    });
    const stage = screen.getByTestId("native-stage");
    Object.defineProperty(stage, "setPointerCapture", { value: vi.fn() });
    // jsdom lacks PointerEvent; MouseEvent retains the pointer's coordinates/button.
    fireEvent(
      stage,
      new MouseEvent("pointerdown", { bubbles: true, clientX: 400, clientY: 300, button: 2 }),
    );
    expect(send).toHaveBeenLastCalledWith(
      JSON.stringify({ type: "pointer", action: "down", x: 0.5, y: 0.5, button: 3 }),
    );
    fireEvent(
      stage,
      new MouseEvent("pointerup", { bubbles: true, clientX: 900, clientY: 700, button: 2 }),
    );
    expect(send).toHaveBeenLastCalledWith(
      JSON.stringify({ type: "pointer", action: "up", x: 1, y: 1, button: 3 }),
    );
  });
  it("preserves control shortcuts instead of typing their letters", async () => {
    const f = fixture();
    render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalled();
    });
    const send = vi.fn();
    firstPeer().ondatachannel?.({
      channel: { label: "workos.input", readyState: "open", send, close: vi.fn() },
    });
    const stage = screen.getByTestId("native-stage");
    fireEvent.keyDown(stage, { key: "c", ctrlKey: true });
    expect(send).toHaveBeenLastCalledWith(JSON.stringify({ type: "key", key: "ctrl+c" }));
    fireEvent.keyDown(stage, { key: "Tab", shiftKey: true });
    expect(send).toHaveBeenLastCalledWith(JSON.stringify({ type: "key", key: "shift+Tab" }));
  });
});

function firstPeer() {
  const peer = Peer.instances[0];
  if (!peer) throw new Error("peer missing");
  return peer;
}
