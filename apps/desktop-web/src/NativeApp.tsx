import { useEffect, useMemo, useRef, useState } from "react";
import { NativeSessionLease } from "./nativeSession.js";
import type { NativeInputEvent } from "@workos/protocol";
import type { WorkOSClients } from "@workos/agent-sdk";
import { Button } from "@workos/ui-kit";

// NativeApp consumes the virtual-display native runner (ADR-0029): one
// owner-scoped WebRTC session per window. The video track renders the real
// Xvfb display; keyboard and pointer events return over the workos.input
// data channel. Without the runner the window states the honest verdict.
export function NativeApp(props: {
  workosClients?: WorkOSClients;
  activeProjectId?: string;
  sessionLease?: NativeSessionLease;
}) {
  const ownLease = useMemo(() => new NativeSessionLease(), []);
  const sessionLease = props.sessionLease ?? ownLease;
  const [status, setStatus] = useState("connecting");
  const [verdict, setVerdict] = useState("");
  const [controls, setControls] = useState(true);
  const [stopping, setStopping] = useState(false);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const channelRef = useRef<RTCDataChannel | null>(null);
  const controlsRef = useRef(true);
  const videoPlayingRef = useRef(false);
  const reconnectRef = useRef<(() => Promise<void>) | undefined>(undefined);
  const handleRef = useRef<ReturnType<NativeSessionLease["acquire"]> | undefined>(undefined);
  const pointerStampRef = useRef(0);

  useEffect(() => {
    if (!clients || !projectId) return;
    let disposed = false;
    const isDisposed = () => disposed;
    const lease = sessionLease.acquire(clients, projectId);
    handleRef.current = lease;
    let session = "";
    let renewal: number | undefined;
    let peer: RTCPeerConnection | undefined;
    let channel: RTCDataChannel | undefined;
    const close = () => {
      window.clearTimeout(renewal);
      peer?.close();
      channel?.close();
      if (channelRef.current === channel) channelRef.current = null;
      lease.release();
    };
    setStatus("connecting");
    setVerdict("");
    setControls(true);
    controlsRef.current = true;
    const run = async () => {
      try {
        session = await lease.session;
        if (isDisposed()) {
          close();
          return;
        }
        if (!session) throw new Error("missing native session");
        const held = await lease.controls.catch(() => true);
        if (isDisposed()) return;
        controlsRef.current = held;
        setControls(held);
        const renew = async () => {
          if (isDisposed()) return;
          setStatus("connecting");
          videoPlayingRef.current = false;
          channelRef.current = null;
          const previous = peer;
          const next = new RTCPeerConnection({});
          peer = next;
          previous?.close();
          next.addTransceiver("video", { direction: "recvonly" });
          // An offer must contain the SCTP m-line before the answerer can open input.
          next.createDataChannel("offer-sctp");
          next.ontrack = (event) => {
            if (disposed || peer !== next) return;
            const [stream] = event.streams;
            const video = videoRef.current;
            if (!video || !stream) return;
            video.srcObject = stream;
          };
          next.ondatachannel = (event) => {
            if (disposed || peer !== next || event.channel.label !== "workos.input") {
              event.channel.close();
              return;
            }
            channel = event.channel;
            channelRef.current = channel;
            channel.onopen = () => {
              if (!disposed && peer === next && videoPlayingRef.current) setStatus("streaming");
            };
            if (channel.readyState === "open" && videoPlayingRef.current) setStatus("streaming");
          };
          next.onconnectionstatechange = () => {
            if (
              !disposed &&
              peer === next &&
              (next.connectionState === "failed" ||
                next.connectionState === "disconnected" ||
                next.connectionState === "closed")
            ) {
              setStatus("ended");
            }
          };
          const offer = await next.createOffer();
          if (isDisposed()) return;
          await next.setLocalDescription(offer);
          await waitIceGathered(next);
          if (isDisposed()) return;
          const connected = await clients.nativeSessions.connectNativeSession({
            sessionId: session,
            controlGeneration: await lease.controlGeneration(),
            offerSdp: next.localDescription?.sdp ?? "",
          });
          if (isDisposed()) return;
          await next.setRemoteDescription({ type: "answer", sdp: connected.answerSdp });
          renewal = window.setTimeout(() => {
            void renew().catch(() => {
              close();
              if (!disposed) setStatus("ended");
            });
          }, 20000);
        };
        reconnectRef.current = renew;
        if (held) await renew();
        else {
          setStatus("unavailable");
          setVerdict(
            "Another device controls this display. Take control to reconnect its screen and input.",
          );
        }
      } catch {
        close();
        if (!disposed) {
          setStatus("unavailable");
          setVerdict("Native sessions are unavailable in this deployment.");
        }
      }
    };
    void run();
    return () => {
      disposed = true;
      reconnectRef.current = undefined;
      close();
    };
  }, [clients, projectId, sessionLease]);

  // The flat scalar payload follows NativeInputEvent protobuf JSON. Derive its
  // fields from the generated contract instead of maintaining a second DTO.
  // Input stays disabled until this device holds the single-controller lease.
  const sendInput = (payload: Partial<Omit<NativeInputEvent, "$typeName" | "$unknown">>) => {
    if (!controlsRef.current) return;
    const channel = channelRef.current;
    if (channel && channel.readyState === "open") {
      if (channel.bufferedAmount > 64 * 1024) return;
      channel.send(JSON.stringify(payload));
    }
  };

  const takeControl = () => {
    const handle = handleRef.current;
    if (!handle) return;
    void handle
      .requestControl()
      .then((held) => {
        controlsRef.current = held;
        setControls(held);
        if (held) {
          setVerdict("");
          void reconnectRef.current?.().catch(() => {
            setStatus("ended");
          });
        }
      })
      .catch(() => undefined);
  };

  const stopWorkload = () => {
    if (stopping) return;
    const handle = handleRef.current;
    if (!handle) return;
    setStopping(true);
    void handle
      .stop()
      .then(() => {
        setStatus("ended");
        setVerdict("The native display was stopped.");
      })
      .catch(() => {
        setVerdict("The native display could not be stopped. Try again.");
      })
      .finally(() => {
        setStopping(false);
      });
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.ctrlKey || event.metaKey || event.altKey) {
      if (
        event.ctrlKey &&
        !event.altKey &&
        !event.metaKey &&
        "cdluaewz".includes(event.key.toLowerCase()) &&
        event.key.length === 1
      ) {
        event.preventDefault();
        sendInput({ type: "key", key: `ctrl+${event.key.toLowerCase()}` });
      }
      return;
    }
    if (event.key === "Tab" && event.shiftKey) {
      event.preventDefault();
      sendInput({ type: "key", key: "shift+Tab" });
      return;
    }
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

  const normalizedPointer = (event: React.PointerEvent<HTMLDivElement>, clamp = false) => {
    const video = videoRef.current;
    if (!video || !video.videoWidth || !video.videoHeight) return;
    const bounds = video.getBoundingClientRect();
    const scale = Math.min(bounds.width / video.videoWidth, bounds.height / video.videoHeight);
    const width = video.videoWidth * scale;
    const height = video.videoHeight * scale;
    const x = (event.clientX - bounds.left - (bounds.width - width) / 2) / width;
    const y = (event.clientY - bounds.top - (bounds.height - height) / 2) / height;
    if (!Number.isFinite(x + y)) return;
    if (clamp) return { x: Math.max(0, Math.min(1, x)), y: Math.max(0, Math.min(1, y)) };
    if (x < 0 || x > 1 || y < 0 || y > 1) return;
    return { x, y };
  };

  // Pointer moves are high frequency; the native input budget is 64 events/s,
  // so the UI throttles to ~40/s before touching the data channel.
  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const now = performance.now();
    if (now - pointerStampRef.current < 25) return;
    pointerStampRef.current = now;
    const point = normalizedPointer(event);
    if (!point) return;
    const { x, y } = point;
    sendInput({ type: "pointer", action: "move", x, y });
  };

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    event.currentTarget.focus();
    event.currentTarget.setPointerCapture(event.pointerId);
    const point = normalizedPointer(event);
    if (!point) return;
    const { x, y } = point;
    const button = event.button + 1;
    sendInput({ type: "pointer", action: "down", x, y, button });
  };

  const onPointerUp = (event: React.PointerEvent<HTMLDivElement>) => {
    // Pointer capture must release a pressed button even outside the video.
    const point = normalizedPointer(event, true);
    if (!point) return;
    const { x, y } = point;
    const button = event.button + 1;
    sendInput({ type: "pointer", action: "up", x, y, button });
  };

  return (
    <div className="native-app" data-testid="native-app">
      <div className="native-toolbar">
        <span data-testid="native-status">{status}</span>
        {!controls && status !== "ended" ? (
          <Button data-testid="native-take-control" onClick={takeControl} type="button">
            Take control
          </Button>
        ) : null}
        {status !== "ended" && status !== "unavailable" ? (
          <Button
            className="native-stop"
            data-testid="native-stop"
            disabled={stopping}
            onClick={stopWorkload}
            type="button"
          >
            {stopping ? "Stopping…" : "Stop"}
          </Button>
        ) : null}
        {!controls && status !== "ended" && status !== "unavailable" ? (
          <span className="native-control-hint" role="status">
            Input is disabled: another device holds control.
          </span>
        ) : null}
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
        onContextMenu={(event) => {
          event.preventDefault();
        }}
        onKeyDown={onKeyDown}
        onPointerDown={onPointerDown}
        onPointerUp={onPointerUp}
        onPointerMove={onPointerMove}
      >
        <video
          ref={videoRef}
          data-testid="native-video"
          onPlaying={() => {
            videoPlayingRef.current = true;
            if (channelRef.current?.readyState === "open") setStatus("streaming");
          }}
          autoPlay
          playsInline
          muted
        />
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
