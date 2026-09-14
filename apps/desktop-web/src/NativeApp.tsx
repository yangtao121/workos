import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";

// NativeApp consumes the virtual-display native runner (ADR-0029): one
// owner-scoped WebRTC session per window. The video track renders the real
// Xvfb display; keyboard and pointer events return over the workos.input
// data channel. Without the runner the window states the honest verdict.
export function NativeApp(props: { workosClients?: WorkOSClients; activeProjectId?: string }) {
  const [status, setStatus] = useState("connecting");
  const [verdict, setVerdict] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [frameCount, setFrameCount] = useState(0);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const channelRef = useRef<RTCDataChannel | null>(null);
  const sessionIdRef = useRef("");
  const pointerStampRef = useRef(0);

  useEffect(() => {
    if (!clients || !projectId || sessionIdRef.current) return;
    const state = { disposed: false };
    // The property check goes through a function so control-flow analysis
    // cannot narrow it to the initial literal before the awaits complete.
    const isDisposed = () => state.disposed;
    const run = async () => {
      try {
        const created = await clients.nativeSessions.createNativeSession({
          idempotencyKey: `desktop-native-${crypto.randomUUID()}`,
          projectId,
          width: 800,
          height: 600,
        });
        if (isDisposed() || !created.session?.id) return;
        sessionIdRef.current = created.session.id;
        setSessionId(created.session.id);

        const peer = new RTCPeerConnection({});
        peer.addTransceiver("video", { direction: "recvonly" });
        peer.ontrack = (event) => {
          const [stream] = event.streams;
          const video = videoRef.current;
          if (!video || !stream) return;
          video.srcObject = stream;
          setStatus("streaming");
        };
        peer.ondatachannel = (event) => {
          if (event.channel.label !== "workos.input") return;
          channelRef.current = event.channel;
        };
        peer.onconnectionstatechange = () => {
          if (peer.connectionState === "failed" || peer.connectionState === "disconnected") {
            setStatus("ended");
          }
        };
        const offer = await peer.createOffer();
        await peer.setLocalDescription(offer);
        // Loopback topology: the runtime answer embeds its complete host
        // candidates, so both sides wait for full gathering before the
        // exchange (no trickle signaling on this path).
        await waitIceGathered(peer);
        const connected = await clients.nativeSessions.connectNativeSession({
          sessionId: created.session.id,
          offerSdp: peer.localDescription?.sdp ?? "",
        });
        if (isDisposed()) return;
        await peer.setRemoteDescription({ type: "answer", sdp: connected.answerSdp });
      } catch {
        if (!isDisposed()) {
          setStatus("unavailable");
          setVerdict("Native sessions are unavailable in this deployment.");
        }
      }
    };
    void run();
    return () => {
      state.disposed = true;
    };
  }, [clients, projectId]);

  // Frame accounting proves live video, not a frozen poster frame.
  useEffect(() => {
    if (!sessionId) return;
    const video = videoRef.current;
    if (!video) return;
    let stopped = false;
    let lastTime = -1;
    const tick = () => {
      if (stopped) return;
      if (video.readyState >= 2 && video.currentTime > 0 && video.currentTime !== lastTime) {
        lastTime = video.currentTime;
        setFrameCount((count) => count + 1);
      }
      window.requestAnimationFrame(tick);
    };
    const handle = window.requestAnimationFrame(tick);
    return () => {
      stopped = true;
      window.cancelAnimationFrame(handle);
    };
  }, [sessionId]);

  // Close the session when the window unmounts.
  useEffect(() => {
    return () => {
      const session = sessionIdRef.current;
      channelRef.current?.close();
      if (session && clients) {
        void clients.nativeSessions.closeNativeSession({ sessionId: session }).then(
          () => undefined,
          () => undefined,
        );
      }
    };
  }, [clients]);

  const sendInput = (payload: unknown) => {
    const channel = channelRef.current;
    if (channel && channel.readyState === "open") {
      channel.send(JSON.stringify(payload));
    }
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key.length === 1) {
      event.preventDefault();
      sendInput({ type: "text", text: event.key });
      return;
    }
    const keyMap: Record<string, string> = {
      Enter: "Return",
      Backspace: "BackSpace",
      Delete: "Delete",
      Escape: "Escape",
      Tab: "Tab",
      ArrowLeft: "Left",
      ArrowRight: "Right",
      ArrowUp: "Up",
      ArrowDown: "Down",
      Home: "Home",
      End: "End",
      PageUp: "Page_Up",
      PageDown: "Page_Down",
    };
    const mapped = keyMap[event.key];
    if (!mapped) return;
    event.preventDefault();
    sendInput({ type: "key", key: mapped });
  };

  const normalizedPointer = (event: React.PointerEvent<HTMLDivElement>) => {
    const bounds = event.currentTarget.getBoundingClientRect();
    const x = (event.clientX - bounds.left) / Math.max(1, bounds.width);
    const y = (event.clientY - bounds.top) / Math.max(1, bounds.height);
    return { x, y };
  };

  // Pointer moves are high frequency; the native input budget is 64 events/s,
  // so the UI throttles to ~40/s before touching the data channel.
  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const now = performance.now();
    if (now - pointerStampRef.current < 25) return;
    pointerStampRef.current = now;
    const { x, y } = normalizedPointer(event);
    sendInput({ type: "pointer", action: "move", x, y });
  };

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    const { x, y } = normalizedPointer(event);
    const button = event.button === 2 ? 2 : event.button === 1 ? 2 : 1;
    sendInput({ type: "pointer", action: "down", x, y, button });
  };

  const onPointerUp = (event: React.PointerEvent<HTMLDivElement>) => {
    const { x, y } = normalizedPointer(event);
    const button = event.button === 2 ? 2 : event.button === 1 ? 2 : 1;
    sendInput({ type: "pointer", action: "up", x, y, button });
  };

  return (
    <div className="native-app" data-testid="native-app">
      <div className="native-toolbar">
        <span data-testid="native-status">{status}</span>
        <span data-testid="native-frames">frames: {frameCount}</span>
        {sessionId ? <span className="native-session">session {sessionId.slice(0, 8)}</span> : null}
      </div>
      {verdict ? (
        <p className="native-verdict" data-testid="native-verdict">
          {verdict}
        </p>
      ) : null}
      <div
        className="native-stage"
        data-testid="native-stage"
        tabIndex={0}
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerUp={onPointerUp}
        onPointerMove={onPointerMove}
      >
        <video ref={videoRef} data-testid="native-video" autoPlay playsInline muted />
      </div>
      <p className="native-hint">Click the stage, then type; input goes to the native display.</p>
    </div>
  );
}

async function waitIceGathered(peer: RTCPeerConnection): Promise<void> {
  if (peer.iceGatheringState === "complete") return;
  await new Promise<void>((resolve) => {
    const timer = window.setTimeout(resolve, 5000);
    peer.addEventListener("icegatheringstatechange", () => {
      if (peer.iceGatheringState === "complete") {
        window.clearTimeout(timer);
        resolve();
      }
    });
  });
}
