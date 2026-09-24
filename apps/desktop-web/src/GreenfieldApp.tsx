import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { attachGreenfieldCompositor, noteKey, releasePressedKeys } from "./greenfieldCompositor.js";

// Greenfield window. The published compositor draws into this canvas.
// Connection failures stay visible; an empty canvas is not reported as the app.
export function GreenfieldApp(props: {
  clients?: WorkOSClients;
  sessionId?: string;
  controlGeneration?: bigint;
  devicePixelRatioMillis?: number;
}) {
  const [status, setStatus] = useState("connecting");
  const [canvasState, setCanvasState] = useState("waiting");
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const clients = props.clients;
  const sessionId = props.sessionId ?? "";

  useEffect(() => {
    if (!clients || !sessionId) {
      setStatus("unavailable");
      return;
    }
    let cancelled = false;
    setStatus("connecting");
    clients.nativeSessions
      .openGreenfieldDisplay({
        sessionId,
        controlGeneration: props.controlGeneration ?? 0n,
        devicePixelRatioMillis: props.devicePixelRatioMillis ?? 1000,
      })
      .then(async (response) => {
        if (cancelled) return;
        if (!response.websocketPath || !response.compositorSessionId) {
          setStatus("unavailable");
          return;
        }
        setStatus("attached");
        const canvas = canvasRef.current;
        if (!canvas) {
          setCanvasState("unavailable");
          return;
        }
        try {
          await attachGreenfieldCompositor({
            canvas,
            launchPath: response.websocketPath,
            compositorSessionId: response.compositorSessionId,
          });
          if (!cancelled) setCanvasState("ready");
        } catch {
          if (!cancelled) setCanvasState("unavailable");
        }
      })
      .catch(() => {
        if (!cancelled) setStatus("unavailable");
      });
    return () => {
      cancelled = true;
    };
  }, [clients, sessionId, props.controlGeneration, props.devicePixelRatioMillis]);

  const label =
    status === "attached"
      ? "已附着原显示会话"
      : status === "connecting"
        ? "正在连接"
        : "显示不可用";
  const pasteText = async () => {
    if (!clients || !sessionId) return;
    let text = "";
    try {
      text = await navigator.clipboard.readText();
    } catch {
      setCanvasState("clipboard-denied");
      return;
    }
    try {
      const response = await clients.nativeSessions.transferNativeClipboard({
        sessionId,
        controlGeneration: props.controlGeneration ?? 0n,
        direction: "host_to_app",
        text: new TextEncoder().encode(text),
      });
      if (response.status !== "ok") setCanvasState("clipboard-denied");
    } catch {
      setCanvasState("clipboard-denied");
    }
  };

  return (
    <section data-testid="greenfield-status" data-status={status} data-canvas={canvasState}>
      <p>{label}</p>
      <canvas
        ref={canvasRef}
        data-testid="greenfield-canvas"
        width={1440}
        height={900}
        tabIndex={0}
        onKeyDown={(event) => noteKey("down", event.key)}
        onKeyUp={(event) => noteKey("up", event.key)}
        onBlur={() => releasePressedKeys()}
      />
      <button type="button" onClick={() => void pasteText()}>
        粘贴文本
      </button>
    </section>
  );
}
