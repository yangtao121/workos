import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type WheelEvent,
} from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  GreenfieldKeyAction,
  GreenfieldPointerAction,
  type GreenfieldWindow,
} from "@workos/protocol";
import { GreenfieldWindowCanvas, type GreenfieldFrameState } from "./GreenfieldWindowCanvas.js";
import {
  GREENFIELD_CLIPBOARD_LIMIT,
  GREENFIELD_TEXT_COMMIT_LIMIT,
  type GreenfieldAttachment,
  type GreenfieldWindowEvent,
  type GreenfieldWindowInputClient,
} from "./greenfieldWindowClient.js";
import type { GreenfieldWindowConnection } from "./greenfieldWindowProjection.js";

function keyEvent(
  action: GreenfieldKeyAction,
  code: string,
  key: string,
  modifiers: {
    ctrl?: boolean;
    alt?: boolean;
    meta?: boolean;
    shift?: boolean;
    repeat?: boolean;
  } = {},
): GreenfieldWindowEvent {
  return { case: "key", value: { action, code, key, ...modifiers } };
}

function shortcutEvents(code: string, key: string): GreenfieldWindowEvent[] {
  return [
    keyEvent(GreenfieldKeyAction.DOWN, code, key, { ctrl: true }),
    keyEvent(GreenfieldKeyAction.UP, code, key, { ctrl: true }),
  ];
}

export function GreenfieldWindowApp(props: {
  client: WorkOSClients["greenfieldWindows"];
  attachment: GreenfieldAttachment;
  input: GreenfieldWindowInputClient;
  nativeWindow: GreenfieldWindow;
  connection: GreenfieldWindowConnection;
  connectionEpoch: number;
}) {
  const { nativeWindow, input, attachment } = props;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const pressedKeys = useRef(new Map<string, GreenfieldWindowEvent>());
  const pressedButtons = useRef(new Set<number>());
  const composing = useRef(false);
  const moving = useRef(false);
  const lastMove = useRef(0);
  const lastResize = useRef("");
  const [frameState, setFrameState] = useState<GreenfieldFrameState>("waiting");
  const [message, setMessage] = useState("");
  const interactive =
    attachment.controls &&
    input.canControl &&
    props.connection === "connected" &&
    frameState === "ready";

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || !interactive || typeof ResizeObserver === "undefined") return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const observer = new ResizeObserver(() => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        const rect = stage.getBoundingClientRect();
        const width = Math.floor(rect.width);
        const height = Math.floor(rect.height);
        if (width < 1 || height < 1) return;
        const dpr = Math.round(window.devicePixelRatio * 1000);
        const size = `${String(width)}:${String(height)}:${String(dpr)}`;
        if (size === lastResize.current) return;
        lastResize.current = size;
        void input
          .send(nativeWindow.id, [
            {
              case: "resize",
              value: { contentWidth: width, contentHeight: height, devicePixelRatioMillis: dpr },
            },
          ])
          .catch(() => {
            setMessage("原生窗口尺寸调整未被接受");
          });
      }, 150);
    });
    observer.observe(stage);
    return () => {
      observer.disconnect();
      clearTimeout(timer);
    };
  }, [interactive, input, nativeWindow.id]);

  const send = (events: GreenfieldWindowEvent[]) => input.send(nativeWindow.id, events);

  const copy = async () => {
    if (!interactive) return;
    setMessage("正在读取原生选区…");
    try {
      const clipboard = (navigator as unknown as { clipboard?: Clipboard }).clipboard;
      if (!clipboard) throw new Error("browser clipboard unavailable");
      await send(shortcutEvents("KeyC", "c"));
      const text = await input.readClipboard();
      if (new TextEncoder().encode(text).byteLength > GREENFIELD_CLIPBOARD_LIMIT)
        throw new Error("clipboard too large");
      await clipboard.writeText(text);
      setMessage("已将当前原生剪贴板文本复制到本机");
    } catch {
      setMessage("复制失败：原生选区不可用，或浏览器拒绝写入剪贴板");
    }
  };

  const paste = async () => {
    if (!interactive) return;
    const clipboard = (navigator as unknown as { clipboard?: Clipboard }).clipboard;
    if (!clipboard) {
      setMessage("粘贴失败：浏览器剪贴板不可用");
      return;
    }
    try {
      // Read while the click or shortcut still holds Chromium user activation.
      const text = await clipboard.readText();
      const bytes = new TextEncoder().encode(text);
      if (bytes.byteLength > GREENFIELD_CLIPBOARD_LIMIT) {
        setMessage("粘贴失败：文本超过 1 MiB");
        return;
      }
      await send([
        { case: "clipboardWrite", value: { textUtf8: bytes } },
        ...shortcutEvents("KeyV", "v"),
      ]);
      setMessage("已向原生应用发送粘贴指令");
    } catch {
      setMessage("粘贴失败：浏览器拒绝读取剪贴板，或原生应用连接已断开");
    }
  };

  const focusNative = () => {
    if (!interactive) return;
    void send([{ case: "focus", value: {} }]).catch(() => {
      setMessage("原生窗口焦点不可用");
    });
  };

  const keyModifiers = (event: KeyboardEvent<HTMLDivElement>) => {
    const editShortcut = event.metaKey && /^[a-z]$/i.test(event.key);
    return {
      ctrl: event.ctrlKey || editShortcut,
      alt: event.altKey,
      meta: editShortcut ? false : event.metaKey,
      shift: event.shiftKey,
      repeat: event.repeat,
    };
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (
      !interactive ||
      composing.current ||
      event.nativeEvent.isComposing ||
      event.key === "Process"
    )
      return;
    if (event.metaKey && !event.ctrlKey && !event.altKey && event.key.toLowerCase() === "c") {
      event.preventDefault();
      void copy();
      return;
    }
    if (event.metaKey && !event.ctrlKey && !event.altKey && event.key.toLowerCase() === "v") {
      event.preventDefault();
      void paste();
      return;
    }
    if (event.code === "MetaLeft" || event.code === "MetaRight") return;
    event.preventDefault();
    const down = keyEvent(GreenfieldKeyAction.DOWN, event.code, event.key, keyModifiers(event));
    pressedKeys.current.set(event.code, down);
    void send([down]).catch(() => {
      setMessage("原生键盘输入未被接受");
    });
  };

  const onKeyUp = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!pressedKeys.current.has(event.code)) return;
    event.preventDefault();
    pressedKeys.current.delete(event.code);
    if (!interactive) return;
    void send([keyEvent(GreenfieldKeyAction.UP, event.code, event.key, keyModifiers(event))]).catch(
      () => {
        setMessage("原生键盘输入未被接受");
      },
    );
  };

  const point = (
    event: PointerEvent<HTMLDivElement> | WheelEvent<HTMLDivElement>,
    clamp = false,
  ) => {
    const canvas = canvasRef.current;
    const visual = nativeWindow.visualRect;
    if (!canvas || !visual || !canvas.width || !canvas.height) return;
    const box = canvas.getBoundingClientRect();
    const scale = Math.min(box.width / canvas.width, box.height / canvas.height);
    const width = canvas.width * scale;
    const height = canvas.height * scale;
    const left = box.left + (box.width - width) / 2;
    const top = box.top + (box.height - height) / 2;
    const x = (event.clientX - left) / width;
    const y = (event.clientY - top) / height;
    if (!Number.isFinite(x) || !Number.isFinite(y)) return;
    if (!clamp && (x < 0 || x > 1 || y < 0 || y > 1)) return;
    return {
      x: Math.max(0, Math.min(1, x)) * visual.width,
      y: Math.max(0, Math.min(1, y)) * visual.height,
    };
  };

  const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (!interactive) return;
    const position = point(event);
    if (!position) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    textareaRef.current?.focus({ preventScroll: true });
    pressedButtons.current.add(event.button);
    void send([
      { case: "focus", value: {} },
      {
        case: "pointer",
        value: { action: GreenfieldPointerAction.DOWN, ...position, button: event.button },
      },
    ]).catch(() => {
      setMessage("原生指针输入未被接受");
    });
  };

  const onPointerUp = (event: PointerEvent<HTMLDivElement>) => {
    if (!pressedButtons.current.delete(event.button) || !interactive) return;
    const position = point(event, true);
    if (!position) return;
    void send([
      {
        case: "pointer",
        value: { action: GreenfieldPointerAction.UP, ...position, button: event.button },
      },
    ]).catch(() => {
      setMessage("原生指针输入未被接受");
    });
  };

  const releaseHeld = () => {
    if (interactive) {
      const releases: GreenfieldWindowEvent[] = [];
      for (const [code, down] of pressedKeys.current) {
        if (down.case !== "key") continue;
        releases.push(keyEvent(GreenfieldKeyAction.UP, code, down.value.key ?? ""));
      }
      for (const button of pressedButtons.current)
        releases.push({
          case: "pointer",
          value: { action: GreenfieldPointerAction.UP, x: 0, y: 0, button },
        });
      if (releases.length) void send(releases).catch(() => undefined);
    }
    pressedKeys.current.clear();
    pressedButtons.current.clear();
  };

  return (
    <div className="greenfield-window-app" data-controller={interactive ? "true" : "false"}>
      <div className="greenfield-window-actions">
        <button type="button" disabled={!interactive} onClick={() => void copy()}>
          复制到本机
        </button>
        <button type="button" disabled={!interactive} onClick={() => void paste()}>
          粘贴到应用
        </button>
        {message ? <span role="status">{message}</span> : null}
      </div>
      <div
        ref={stageRef}
        className="greenfield-window-input-stage"
        onContextMenu={(event) => {
          event.preventDefault();
        }}
        onFocusCapture={focusNative}
        onKeyDown={onKeyDown}
        onKeyUp={onKeyUp}
        onBlurCapture={releaseHeld}
        onPointerDown={onPointerDown}
        onPointerUp={onPointerUp}
        onPointerMove={(event) => {
          if (!interactive || moving.current || performance.now() - lastMove.current < 25) return;
          const position = point(event);
          if (!position) return;
          lastMove.current = performance.now();
          moving.current = true;
          void send([
            { case: "pointer", value: { action: GreenfieldPointerAction.MOVE, ...position } },
          ])
            .catch(() => undefined)
            .finally(() => {
              moving.current = false;
            });
        }}
        onWheel={(event) => {
          if (!interactive) return;
          const position = point(event);
          if (!position) return;
          event.preventDefault();
          void send([
            {
              case: "pointer",
              value: {
                action: GreenfieldPointerAction.WHEEL,
                ...position,
                deltaX: event.deltaX,
                deltaY: event.deltaY,
              },
            },
          ]).catch(() => undefined);
        }}
      >
        <GreenfieldWindowCanvas
          client={props.client}
          attachment={attachment}
          nativeWindow={nativeWindow}
          connectionEpoch={props.connectionEpoch}
          canvasRef={canvasRef}
          onStateChange={setFrameState}
        />
        {props.connection !== "connected" && frameState === "ready" ? (
          <div className="greenfield-window-overlay" role="status">
            {props.connection === "unavailable"
              ? "原生窗口连接不可用"
              : "原生窗口连接中断，正在重连"}
          </div>
        ) : null}
        <textarea
          ref={textareaRef}
          className="greenfield-ime"
          aria-label="原生窗口输入"
          disabled={!interactive}
          autoCapitalize="none"
          autoComplete="off"
          spellCheck={false}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={(event) => {
            composing.current = false;
            const text = event.data;
            event.currentTarget.value = "";
            if (!interactive || !text) return;
            if (new TextEncoder().encode(text).byteLength > GREENFIELD_TEXT_COMMIT_LIMIT) {
              setMessage("输入失败：输入法文本超过 16 KiB");
              return;
            }
            void send([{ case: "text", value: { text } }]).catch(() => {
              setMessage("输入失败：原生连接或控制权不可用");
            });
          }}
        />
      </div>
      <p className="native-hint">
        {props.connection !== "connected" || frameState !== "ready"
          ? "原生窗口画面暂不可用"
          : interactive
            ? "点击画面输入；Mac 使用 Cmd+C／Cmd+V，Ctrl+C 仍由应用处理。"
            : "只读观察；当前设备没有输入控制权。"}
      </p>
    </div>
  );
}
