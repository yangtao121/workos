// @vitest-environment jsdom
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
  constructor(readonly configuration?: RTCConfiguration) {
    Peer.instances.push(this);
  }
}
function fixture(create = vi.fn(() => Promise.resolve({ session: { id: "session-1" } }))) {
  const nativeSessions = {
    getNativeConnectivity: vi.fn(() =>
      Promise.resolve({
        mode: "loopback",
        relayOnly: false,
        iceServers: [] as { urls: string[]; username: string; credential: string }[],
        expiresAt: { seconds: BigInt(Math.floor(Date.now() / 1000) + 90), nanos: 0 },
      }),
    ),
    createNativeSession: create,
    connectNativeSession: vi.fn(() => Promise.resolve({ answerSdp: "answer" })),
    closeNativeSession: vi.fn(() => Promise.resolve({})),
    detachNativeSession: vi.fn(() => Promise.resolve({})),
  };
  // No running workload: creation acquires the first controller epoch.
  const surfaceContinuity = {
    listProjectSurfaces: vi.fn<() => Promise<unknown>>(() => Promise.resolve({ workloads: [] })),
    attachSurface: vi.fn<() => Promise<unknown>>(() =>
      Promise.resolve({ attachment: { controls: true, controlGeneration: 1n } }),
    ),
    requestSurfaceControl: vi.fn<() => Promise<unknown>>(() => Promise.resolve({})),
    stopSurfaceWorkload: vi.fn<() => Promise<unknown>>(() => Promise.resolve({})),
  };
  vi.stubGlobal("RTCPeerConnection", Peer);
  return {
    nativeSessions,
    surfaceContinuity,
    clients: { nativeSessions, surfaceContinuity } as unknown as WorkOSClients,
  };
}
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Peer.instances = [];
});

describe("Native window lifecycle and input", () => {
  it("sends committed touch text once and retains a draft when the input channel is full", async () => {
    const f = fixture();
    render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalled();
    });
    const send = vi.fn();
    const channel = {
      label: "workos.input",
      readyState: "open",
      bufferedAmount: 0,
      send,
      close: vi.fn(),
    };
    firstPeer().ondatachannel?.({ channel });
    fireEvent.playing(screen.getByTestId("native-video"));
    const input = screen.getByLabelText<HTMLInputElement>("Native text");
    const form = screen.getByTestId("native-touch-controls");
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: "你好" } });
    fireEvent.submit(form);
    expect(send).not.toHaveBeenCalled();
    fireEvent.compositionEnd(input);
    fireEvent.submit(form);
    expect(send).toHaveBeenCalledExactlyOnceWith(JSON.stringify({ type: "text", text: "你好" }));
    expect(input.value).toBe("");
    await userEvent.click(screen.getByRole("button", { name: "Enter" }));
    expect(send).toHaveBeenLastCalledWith(JSON.stringify({ type: "key", key: "Return" }));
    channel.bufferedAmount = 100000;
    fireEvent.change(input, { target: { value: "retained draft" } });
    fireEvent.submit(form);
    expect(input.value).toBe("retained draft");
    expect(send).toHaveBeenCalledTimes(2);
  });
  it("uses the issued relay policy and refuses expired transport capabilities", async () => {
    const f = fixture();
    f.nativeSessions.getNativeConnectivity.mockResolvedValueOnce({
      mode: "relay",
      relayOnly: true,
      iceServers: [
        {
          urls: ["turn:relay.fixture:3478"],
          username: "ephemeral",
          credential: "fixture-capability",
        },
      ],
      expiresAt: { seconds: BigInt(Math.floor(Date.now() / 1000) + 90), nanos: 0 },
    });
    const view = render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalledOnce();
    });
    expect(firstPeer().configuration?.iceTransportPolicy).toBe("relay");
    expect(firstPeer().configuration?.iceServers?.[0]?.username).toBe("ephemeral");
    view.unmount();
    const expired = fixture();
    expired.nativeSessions.getNativeConnectivity.mockResolvedValue({
      mode: "relay",
      relayOnly: true,
      iceServers: [],
      expiresAt: { seconds: 1n, nanos: 0 },
    });
    render(<NativeApp workosClients={expired.clients} activeProjectId="another-project" />);
    await waitFor(() => {
      expect(screen.getByTestId("native-status").textContent).toBe("unavailable");
    });
    expect(expired.nativeSessions.connectNativeSession).not.toHaveBeenCalled();
    expect(Peer.instances).toHaveLength(1);
  });
  it("negotiates SCTP and detaches (never closes) the session on unmount", async () => {
    const f = fixture();
    const view = render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalled();
    });
    const peer = firstPeer();
    expect(f.nativeSessions.getNativeConnectivity).toHaveBeenCalledWith({
      sessionId: "session-1",
      controlGeneration: 1n,
    });
    expect(peer.createDataChannel).toHaveBeenCalled();
    view.unmount();
    expect(peer.close).toHaveBeenCalledOnce();
    await waitFor(() => {
      expect(f.nativeSessions.detachNativeSession).toHaveBeenCalledWith({ sessionId: "session-1" });
    });
    expect(f.nativeSessions.closeNativeSession).not.toHaveBeenCalled();
  });
  it("detaches a creation response arriving after the window disappeared", async () => {
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
    // Discovery finishes before creating a program.
    await waitFor(() => {
      expect(f.nativeSessions.createNativeSession).toHaveBeenCalled();
    });
    view.unmount();
    complete({ session: { id: "late-session" } });
    await waitFor(() => {
      expect(f.nativeSessions.detachNativeSession).toHaveBeenCalledWith({
        sessionId: "late-session",
      });
    });
    expect(f.nativeSessions.closeNativeSession).not.toHaveBeenCalled();
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
      expect(f.nativeSessions.detachNativeSession).toHaveBeenCalledOnce();
    });
  });
  it("restores an exact native workload and never falls back after it disappeared", async () => {
    const f = fixture();
    f.surfaceContinuity.attachSurface.mockRejectedValue(new ConnectError("stopped", Code.NotFound));
    render(
      <NativeApp
        workosClients={f.clients}
        activeProjectId="project"
        workloadId="exact-native"
        expectedWorkloadGeneration={3n}
      />,
    );
    await waitFor(() => {
      expect(screen.getByTestId("native-status").textContent).toBe("unavailable");
    });
    expect(f.surfaceContinuity.attachSurface).toHaveBeenCalledWith(
      expect.objectContaining({ workloadId: "exact-native", expectedWorkloadGeneration: 3n }),
    );
    expect(f.surfaceContinuity.listProjectSurfaces).not.toHaveBeenCalled();
    expect(f.nativeSessions.createNativeSession).not.toHaveBeenCalled();
  });
  it("reattaches the project's live native workload instead of creating one", async () => {
    const f = fixture();
    f.surfaceContinuity.listProjectSurfaces = vi.fn<() => Promise<unknown>>(() =>
      Promise.resolve({
        workloads: [
          {
            workloadId: "workload-1",
            projectId: "project",
            appInstanceId: "",
            renderer: 4,
            state: "running",
            displayName: "Native display",
            attachmentCount: 1,
          },
        ],
      }),
    );
    f.surfaceContinuity.attachSurface = vi.fn<() => Promise<unknown>>(() =>
      Promise.resolve({
        session: { id: "workload-1" },
        attachment: { controls: false, surfaceSessionId: "workload-1" },
      }),
    );
    render(<NativeApp workosClients={f.clients} activeProjectId="project" />);
    await waitFor(() => {
      expect(f.surfaceContinuity.attachSurface).toHaveBeenCalled();
    });
    expect(f.nativeSessions.createNativeSession).not.toHaveBeenCalled();
    expect(f.nativeSessions.connectNativeSession).not.toHaveBeenCalled();
    // Observer attachment: the takeover button is the only way to input.
    expect(screen.getByTestId("native-take-control")).toBeTruthy();
    f.surfaceContinuity.requestSurfaceControl = vi.fn<() => Promise<unknown>>(() =>
      Promise.resolve({ attachment: { controls: true, controlGeneration: 2n } }),
    );
    await userEvent.click(screen.getByTestId("native-take-control"));
    await waitFor(() => {
      expect(screen.queryByTestId("native-take-control")).toBeNull();
    });
    await waitFor(() => {
      expect(f.nativeSessions.connectNativeSession).toHaveBeenCalledWith(
        expect.objectContaining({ sessionId: "workload-1", controlGeneration: 2n }),
      );
    });
    // The explicit stop affordance is the only stop path.
    await userEvent.click(screen.getByTestId("native-stop"));
    await waitFor(() => {
      expect(f.surfaceContinuity.stopSurfaceWorkload).toHaveBeenCalledWith(
        expect.objectContaining({ workloadId: "workload-1" }),
      );
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
