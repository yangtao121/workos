import { useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  attachGreenfieldCompositor,
  MAX_GREENFIELD_CLIPBOARD_BYTES,
  type GreenfieldBridge,
} from "./greenfieldCompositor.js";

// A Greenfield connection is a view of the supervised Code workload. Clipboard
// buttons and macOS shortcuts act on the real Wayland text selection.
export function GreenfieldApp(props: {
  clients?: WorkOSClients;
  sessionId?: string;
  controlGeneration?: bigint;
  controls?: boolean;
  devicePixelRatioMillis?: number;
}) {
  const [status, setStatus] = useState("connecting");
  const [canvasState, setCanvasState] = useState("waiting");
  const [clipboardResult, setClipboardResult] = useState("");
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const inputRef = useRef<HTMLTextAreaElement | null>(null);
  const bridgeRef = useRef<GreenfieldBridge | null>(null);
  const controlRef = useRef(props.controls ?? true);
  const maxBytesRef = useRef(MAX_GREENFIELD_CLIPBOARD_BYTES);
  const clients = props.clients;
  const sessionId = props.sessionId ?? "";
  const controls = props.controls ?? true;
  controlRef.current = controls;

  const copyFromApp = async () => {
    const bridge = bridgeRef.current;
    if (!bridge || !controlRef.current) {
      setClipboardResult("复制失败：显示连接或控制权不可用");
      return;
    }
    setClipboardResult("正在读取应用选区…");
    try {
      const text = await bridge.copyFromApp();
      if (new TextEncoder().encode(text).byteLength > maxBytesRef.current) {
        setClipboardResult("复制失败：文本超过 1 MiB");
        return;
      }
      const clipboard = (navigator as { clipboard?: Clipboard }).clipboard;
      if (!clipboard) throw new Error("clipboard unavailable");
      await clipboard.writeText(text);
      setClipboardResult("已复制到本机剪贴板");
    } catch {
      setClipboardResult("复制失败：应用没有提供文本选区，或浏览器拒绝剪贴板写入");
    }
  };

  const pasteIntoApp = async () => {
    const bridge = bridgeRef.current;
    if (!bridge || !controlRef.current) {
      setClipboardResult("粘贴失败：显示连接或控制权不可用");
      return;
    }
    const clipboard = (navigator as { clipboard?: Clipboard }).clipboard;
    if (!clipboard) {
      setClipboardResult("粘贴失败：此浏览器连接不能读取剪贴板");
      return;
    }
    try {
      // Call readText during the actual click/key gesture, before any await.
      const text = await clipboard.readText();
      if (new TextEncoder().encode(text).byteLength > maxBytesRef.current) {
        setClipboardResult("粘贴失败：文本超过 1 MiB");
        return;
      }
      bridge.pasteIntoApp(text);
      setClipboardResult("已向应用发送粘贴指令");
    } catch {
      setClipboardResult("粘贴失败：浏览器拒绝读取剪贴板，或应用连接已断开");
    }
  };

  useEffect(() => {
    bridgeRef.current?.setControlling(controls);
  }, [controls]);

  useEffect(() => {
    if (!clients || !sessionId) {
      setStatus("unavailable");
      return;
    }
    if (!controls) {
      setStatus("observer");
      setCanvasState("unavailable");
      return;
    }
    let cancelled = false;
    const isCancelled = () => cancelled;
    let attached: GreenfieldBridge | null = null;
    setStatus("connecting");
    setCanvasState("waiting");
    setClipboardResult("");
    clients.nativeSessions
      .openGreenfieldDisplay({
        sessionId,
        controlGeneration: props.controlGeneration ?? 0n,
        devicePixelRatioMillis:
          props.devicePixelRatioMillis ?? Math.round(window.devicePixelRatio * 1000),
      })
      .then(async (response) => {
        if (isCancelled()) return;
        if (!response.websocketPath || !response.compositorSessionId) {
          setStatus("unavailable");
          return;
        }
        const canvas = canvasRef.current;
        const textarea = inputRef.current;
        if (!canvas || !textarea) {
          setStatus("unavailable");
          return;
        }
        canvas.width = response.width > 0 ? response.width : 1440;
        canvas.height = response.height > 0 ? response.height : 900;
        maxBytesRef.current = response.clipboardMaxBytes || MAX_GREENFIELD_CLIPBOARD_BYTES;
        setStatus("attached");
        try {
          attached = await attachGreenfieldCompositor({
            canvas,
            textarea,
            launchPath: response.websocketPath,
            compositorSessionId: response.compositorSessionId,
            options: {
              canControl: () => controlRef.current,
              onCopyShortcut: () => void copyFromApp(),
              onPasteShortcut: () => void pasteIntoApp(),
              onCompositionCommitted: (result) => {
                if (result === "sent") setClipboardResult("已向应用发送输入法文本");
                else if (result === "too_large") setClipboardResult("输入失败：文本超过 1 MiB");
                else setClipboardResult("输入失败：显示连接或控制权不可用");
              },
              onConnectionStateChange: (next) => {
                if (isCancelled()) return;
                setCanvasState(next === "open" ? "connected" : "unavailable");
                if (next !== "open") setClipboardResult("显示通道中断，请等待重新连接");
              },
              maxClipboardBytes: maxBytesRef.current,
            },
          });
          if (isCancelled()) {
            attached.dispose();
            return;
          }
          bridgeRef.current = attached;
        } catch {
          if (!isCancelled()) setCanvasState("unavailable");
        }
      })
      .catch(() => {
        if (!isCancelled()) setStatus("unavailable");
      });
    return () => {
      cancelled = true;
      attached?.dispose();
      if (bridgeRef.current === attached) bridgeRef.current = null;
    };
  }, [clients, sessionId, props.controlGeneration, props.devicePixelRatioMillis, controls]);

  const label =
    status === "attached"
      ? canvasState === "connected"
        ? "显示通道已连接"
        : canvasState === "unavailable"
          ? "显示通道连接失败"
          : "正在连接显示通道"
      : status === "observer"
        ? "当前设备没有控制权；此显示暂不能提供只读观察"
        : status === "connecting"
          ? "正在连接"
          : "显示不可用";

  return (
    <section
      className="greenfield-app"
      data-testid="greenfield-status"
      data-status={status}
      data-canvas={canvasState}
    >
      <div className="greenfield-toolbar">
        <span role="status">{label}</span>
        <button
          type="button"
          disabled={!controls || canvasState !== "connected"}
          onClick={() => void copyFromApp()}
        >
          复制到本机
        </button>
        <button
          type="button"
          disabled={!controls || canvasState !== "connected"}
          onClick={() => void pasteIntoApp()}
        >
          粘贴到应用
        </button>
      </div>
      <div className="greenfield-stage">
        <canvas
          ref={canvasRef}
          data-testid="greenfield-canvas"
          width={1440}
          height={900}
          tabIndex={0}
          onFocus={() => inputRef.current?.focus({ preventScroll: true })}
        />
        <textarea
          ref={inputRef}
          className="greenfield-ime"
          aria-label="原生应用输入"
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          tabIndex={-1}
          disabled={!controls || canvasState !== "connected"}
        />
      </div>
      {clipboardResult ? (
        <p className="greenfield-clipboard-result" role="status">
          {clipboardResult}
        </p>
      ) : null}
      <p className="native-hint">点击画面直接输入；Mac 使用 Cmd+C／Cmd+V，Ctrl+C 仍由应用处理。</p>
    </section>
  );
}
