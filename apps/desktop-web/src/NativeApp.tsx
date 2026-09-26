import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { NativeSessionLease } from "./nativeSession.js";
import type { NativeInputEvent } from "@workos/protocol";
import type { WorkOSClients } from "@workos/agent-sdk";
import { Button } from "@workos/ui-kit";
import type {
  GreenfieldWindowInputClient,
  GreenfieldAttachment,
} from "./greenfieldWindowClient.js";
import {
  GreenfieldWindowProjection,
  type GreenfieldWindowProjectionState,
} from "./greenfieldWindowProjection.js";

export interface GreenfieldViewerState {
  attachment: GreenfieldAttachment;
  input: GreenfieldWindowInputClient;
  projection: GreenfieldWindowProjectionState;
  requestControl: () => Promise<void>;
}

// NativeApp consumes the virtual-display native runner (ADR-0029): one
// owner-scoped WebRTC session per window. The video track renders the real
// Xvfb display; keyboard and pointer events return over the workos.input
// data channel. Without the runner the window states the honest verdict.
export function NativeApp(props: {
  workosClients?: WorkOSClients;
  activeProjectId?: string;
  sessionLease?: NativeSessionLease | undefined;
  workloadId?: string | undefined;
  expectedWorkloadGeneration?: bigint | undefined;
  onGreenfieldViewer?: (viewer?: GreenfieldViewerState) => void;
}) {
  const ownLease = useMemo(() => new NativeSessionLease(), []);
  const sessionLease = props.sessionLease ?? ownLease;
  const [stopped, setStopped] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [status, setStatus] = useState("connecting");
  const [verdict, setVerdict] = useState("");
  const [controls, setControls] = useState(true);
  const [stopping, setStopping] = useState(false);
  const [inputDraft, setInputDraft] = useState("");
  const [greenfieldSession, setGreenfieldSession] = useState("");
  const [greenfieldProjection, setGreenfieldProjection] =
    useState<GreenfieldWindowProjectionState>();
  const viewerRef = useRef<GreenfieldViewerState | undefined>(undefined);
  const viewerCallbackRef = useRef(props.onGreenfieldViewer);
  viewerCallbackRef.current = props.onGreenfieldViewer;
  const composingRef = useRef(false);
  const clients = props.workosClients;
  const projectId = props.activeProjectId ?? "";
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const channelRef = useRef<RTCDataChannel | null>(null);
  const controlsRef = useRef(true);
  const videoPlayingRef = useRef(false);
  const reconnectRef = useRef<(() => Promise<void>) | undefined>(undefined);
  const handleRef = useRef<ReturnType<NativeSessionLease["acquire"]> | undefined>(undefined);
  const pointerStampRef = useRef(0);

  // The real top-level windows can cover the lifecycle host. Keep their
  // takeover action tied to this same lease so a responsive remount retains
  // the updated control generation and cannot reuse a stale input epoch.
  const requestControl = useCallback(async (): Promise<void> => {
    const handle = handleRef.current;
    if (!handle) throw new Error("native attachment unavailable");
    const held = await handle.requestControl();
    controlsRef.current = held;
    setControls(held);
    if (!held) throw new Error("native control was not granted");
    setVerdict("");
    const viewer = viewerRef.current;
    if (viewer) {
      if ((await handle.attachmentId()) !== viewer.attachment.attachmentId) {
        // Explicit recovery created a new attachment and input sequence. The
        // old window projection must close before the new one starts.
        setAttempt((current) => current + 1);
        return;
      }
      const generation = await handle.controlGeneration();
      viewer.input.setControl(true, generation);
      const updated: GreenfieldViewerState = {
        ...viewer,
        attachment: { ...viewer.attachment, controls: true, controlGeneration: generation },
      };
      viewerRef.current = updated;
      viewerCallbackRef.current?.(updated);
      setStatus("attached");
      return;
    }
    try {
      await reconnectRef.current?.();
    } catch (error) {
      setStatus("reconnecting");
      setAttempt((current) => current + 1);
      throw error;
    }
  }, []);

  useEffect(() => {
    if (!clients || !projectId || stopped) return;
    setInputDraft("");
    composingRef.current = false;
    let disposed = false;
    const isDisposed = () => disposed;
    const lease = sessionLease.acquire(
      clients,
      projectId,
      props.workloadId,
      props.expectedWorkloadGeneration,
    );
    handleRef.current = lease;
    const unsubscribeControl = lease.onControlChange((held) => {
      if (disposed || handleRef.current !== lease || held) return;
      controlsRef.current = false;
      setControls(false);
      setVerdict("输入控制已失效。请显式接管以恢复输入。");
      const viewer = viewerRef.current;
      if (!viewer) return;
      viewer.input.setControl(false, viewer.attachment.controlGeneration);
      const updated: GreenfieldViewerState = {
        ...viewer,
        attachment: { ...viewer.attachment, controls: false },
      };
      viewerRef.current = updated;
      viewerCallbackRef.current?.(updated);
    });
    let session = "";
    let renewal: number | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let peer: RTCPeerConnection | undefined;
    let channel: RTCDataChannel | undefined;
    let windowProjection: GreenfieldWindowProjection | undefined;
    let unsubscribeProjection: (() => void) | undefined;
    const close = () => {
      window.clearTimeout(renewal);
      clearTimeout(retry);
      peer?.close();
      channel?.close();
      if (channelRef.current === channel) channelRef.current = null;
      unsubscribeProjection?.();
      windowProjection?.stop();
      if (viewerRef.current) {
        viewerRef.current = undefined;
        viewerCallbackRef.current?.(undefined);
      }
      unsubscribeControl();
      lease.release();
    };
    setStatus("connecting");
    setVerdict("");
    setControls(true);
    controlsRef.current = true;
    setGreenfieldSession("");
    setGreenfieldProjection(undefined);
    const run = async () => {
      try {
        session = await lease.session;
        if (isDisposed()) {
          close();
          return;
        }
        if (!session) throw new Error("missing native session");
        const held = await lease.controls.catch(() => false);
        if (isDisposed()) return;
        controlsRef.current = held;
        setControls(held);
        const readSession = clients.nativeSessions.getNativeSession;
        if (typeof readSession === "function") {
          const facts = await readSession({ sessionId: session });
          if (!isDisposed() && facts.session?.engine === "greenfield") {
            const attachmentId = await lease.attachmentId();
            const workloadGeneration = await lease.workloadGeneration();
            const controlGeneration = await lease.controlGeneration();
            if (!attachmentId || workloadGeneration < 1n)
              throw new ConnectError("resident Greenfield viewer unavailable", Code.Unimplemented);
            const attachment: GreenfieldAttachment = {
              sessionId: session,
              attachmentId,
              workloadGeneration,
              controlGeneration,
              controls: held,
            };
            const input = sessionLease.greenfieldInputClient(clients.greenfieldWindows, attachment);
            windowProjection = new GreenfieldWindowProjection(
              clients.greenfieldWindows,
              attachment,
            );
            unsubscribeProjection = windowProjection.subscribe((projection) => {
              if (isDisposed()) return;
              setGreenfieldProjection(projection);
              viewerRef.current = {
                attachment: { ...(viewerRef.current?.attachment ?? attachment) },
                input,
                projection,
                requestControl,
              };
              viewerCallbackRef.current?.(viewerRef.current);
            });
            windowProjection.start();
            setGreenfieldSession(session);
            setStatus("attached");
            if (!held) {
              setVerdict("只读观察：另一设备持有输入控制权。");
            }
            return;
          }
        }
        const renew = async () => {
          if (isDisposed()) return;
          setStatus("connecting");
          videoPlayingRef.current = false;
          channelRef.current = null;
          const previous = peer;
          const generation = await lease.controlGeneration();
          const connectivity = await clients.nativeSessions.getNativeConnectivity({
            sessionId: session,
            controlGeneration: generation,
          });
          if (isDisposed()) return;
          if (
            !connectivity.expiresAt ||
            Number(connectivity.expiresAt.seconds) * 1000 <= Date.now() ||
            (connectivity.relayOnly && connectivity.iceServers.length === 0)
          ) {
            throw new ConnectError(
              "native connection capability expired or unavailable",
              Code.FailedPrecondition,
            );
          }
          const next = new RTCPeerConnection({
            iceTransportPolicy: connectivity.relayOnly ? "relay" : "all",
            iceServers: connectivity.iceServers.map((server) => ({
              urls: server.urls,
              username: server.username,
              credential: server.credential,
            })),
          });
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
              setStatus("reconnecting");
              window.clearTimeout(renewal);
              clearTimeout(retry);
              retry = setTimeout(() => {
                if (!disposed) setAttempt((current) => current + 1);
              }, 1500);
            }
          };
          const offer = await next.createOffer();
          if (isDisposed()) return;
          await next.setLocalDescription(offer);
          await waitIceGathered(next);
          if (isDisposed()) return;
          const connected = await clients.nativeSessions.connectNativeSession({
            sessionId: session,
            controlGeneration: generation,
            offerSdp: next.localDescription?.sdp ?? "",
          });
          if (isDisposed()) return;
          await next.setRemoteDescription({ type: "answer", sdp: connected.answerSdp });
          renewal = window.setTimeout(() => {
            void renew().catch(() => {
              close();
              if (!disposed) {
                setStatus("reconnecting");
                retry = setTimeout(() => {
                  setAttempt((current) => current + 1);
                }, 1500);
              }
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
      } catch (error) {
        close();
        if (!disposed) {
          const terminal =
            error instanceof ConnectError &&
            [
              Code.NotFound,
              Code.PermissionDenied,
              Code.Unauthenticated,
              Code.FailedPrecondition,
              Code.Unimplemented,
            ].includes(error.code);
          setStatus(terminal ? "unavailable" : "reconnecting");
          setVerdict(
            terminal
              ? "This display is stopped or no longer accessible. Open Running apps to inspect it."
              : "Connection interrupted. Reconnecting to this display…",
          );
          if (!terminal)
            retry = setTimeout(() => {
              setAttempt((current) => current + 1);
            }, 1500);
        }
      }
    };
    void run();
    return () => {
      disposed = true;
      reconnectRef.current = undefined;
      close();
    };
  }, [
    clients,
    projectId,
    sessionLease,
    props.workloadId,
    props.expectedWorkloadGeneration,
    attempt,
    stopped,
    requestControl,
  ]);

  // The flat scalar payload follows NativeInputEvent protobuf JSON. Derive its
  // fields from the generated contract instead of maintaining a second DTO.
  // Input stays disabled until this device holds the single-controller lease.
  const sendInput = (payload: Partial<Omit<NativeInputEvent, "$typeName" | "$unknown">>) => {
    if (!controlsRef.current) return false;
    const channel = channelRef.current;
    if (channel && channel.readyState === "open") {
      if (channel.bufferedAmount > 64 * 1024) return false;
      try {
        channel.send(JSON.stringify(payload));
        return true;
      } catch {
        return false;
      }
    }
    return false;
  };

  const takeControl = () => {
    void requestControl().catch(() => {
      setVerdict("Taking control failed. Check the connection and try again.");
    });
  };

  const stopWorkload = () => {
    if (stopping) return;
    const handle = handleRef.current;
    if (!handle) return;
    setStopping(true);
    void handle
      .stop()
      .then(() => {
        setStopped(true);
        setControls(false);
        controlsRef.current = false;
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
      {greenfieldSession ? (
        <p className="native-greenfield-status" role="status">
          {greenfieldProjection?.connection === "connected"
            ? greenfieldProjection.snapshot?.windows.length
              ? "原生窗口已显示在 WorkOS 桌面中"
              : "原生应用运行中，等待窗口"
            : greenfieldProjection?.connection === "unavailable"
              ? "原生窗口服务不可用"
              : "正在连接原生窗口服务"}
        </p>
      ) : null}
      <div
        className="native-stage"
        data-testid="native-stage"
        hidden={Boolean(greenfieldSession)}
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
      <form
        className="native-touch-controls"
        data-testid="native-touch-controls"
        hidden={Boolean(greenfieldSession)}
        onSubmit={(event) => {
          event.preventDefault();
          if (composingRef.current || !inputDraft) return;
          if (sendInput({ type: "text", text: inputDraft })) setInputDraft("");
        }}
      >
        <input
          aria-label="Native text"
          placeholder="Text for the remote app"
          value={inputDraft}
          maxLength={256}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          disabled={!controls || status !== "streaming"}
          onChange={(event) => {
            setInputDraft(event.target.value);
          }}
          onCompositionStart={() => {
            composingRef.current = true;
          }}
          onCompositionEnd={() => {
            composingRef.current = false;
          }}
        />
        <Button type="submit" disabled={!controls || status !== "streaming" || !inputDraft}>
          Send text
        </Button>
        <div className="native-touch-keys">
          {(
            [
              ["Enter", "Return"],
              ["Backspace", "BackSpace"],
              ["Tab", "Tab"],
              ["Esc", "Escape"],
              ["Ctrl+C", "ctrl+c"],
            ] as const
          ).map(([label, key]) => (
            <Button
              key={key}
              type="button"
              disabled={!controls || status !== "streaming"}
              onClick={() => {
                sendInput({ type: "key", key });
              }}
            >
              {label}
            </Button>
          ))}
        </div>
      </form>
      <p className="native-hint">
        {greenfieldSession
          ? "在 WorkOS 原生窗口中查看画面；关闭窗口会断开此设备，Code 继续运行。"
          : "Click the stage, then type; input goes to the native display."}
      </p>
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
