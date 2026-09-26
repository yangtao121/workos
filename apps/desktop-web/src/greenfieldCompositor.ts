import { webInputOutput } from "@gfld/compositor/dist/web/WebInputOutput.js";

// The pinned compositor's initScene installs background clipboard reads and
// writes. Bind the scene directly so every text transfer has a user gesture
// and a result that the UI can report.
export const MAX_GREENFIELD_CLIPBOARD_BYTES = 1024 * 1024;
const textEncoder = new TextEncoder();

type Key = {
  keyCode: { evdevKeyCode: number; x11KeyCode: number };
  timeStamp: number;
  pressed: boolean;
  capsLock: boolean;
  numLock: boolean;
};
type Button = {
  x: number;
  y: number;
  timestamp: number;
  buttonCode: number;
  released: boolean;
  buttons: number;
  sceneId: string;
};
type Axis = {
  deltaMode: number;
  DOM_DELTA_LINE: number;
  DOM_DELTA_PAGE: number;
  DOM_DELTA_PIXEL: number;
  deltaX: number;
  deltaY: number;
  timestamp: number;
  sceneId: string;
};
type DataSource = {
  mimeTypes: string[];
  inputOutput: {
    mkfifo(): Promise<
      [
        {
          readBlob(): Promise<Blob>;
          close(): Promise<void>;
        },
        {
          close(): Promise<void>;
        },
      ]
    >;
  };
  send(mimeType: string, writeFD: unknown): void;
};
type WriteFD = { write(data: Blob): Promise<void>; close(): Promise<void> };

// Greenfield's text data-source contract is internal in rc1. Keep this small
// adapter beside the scene binding so the native app reads committed text from
// a Wayland offer instead of a runtime-side memory string.
class BrowserTextSource {
  readonly mimeTypes = ["text/plain;charset=utf-8", "text/plain"];
  readonly inputOutput = webInputOutput;
  readonly version = 3;
  accepted = false;
  compositorAction = 0;
  currentDndAction = 0;
  dndActions = 0;
  setSelection = false;
  dataOffer: unknown;
  private listeners: Array<() => void> = [];
  private resolveDestroy!: () => void;
  private destroyPromise = new Promise<void>((resolve) => {
    this.resolveDestroy = resolve;
  });

  constructor(private readonly text: string) {}

  send(mimeType: string, fd: WriteFD) {
    if (!this.mimeTypes.includes(mimeType)) {
      void fd.close();
      return;
    }
    void fd.write(new Blob([textEncoder.encode(this.text)])).then(() => fd.close());
  }
  accept() {}
  action() {}
  cancel() {}
  dndDropPerformed() {}
  notifyFinish() {
    this.dataOffer = undefined;
  }
  addDestroyListener(listener: () => void) {
    this.listeners.push(listener);
  }
  removeDestroyListener(listener: () => void) {
    this.listeners = this.listeners.filter((candidate) => candidate !== listener);
  }
  destroyDataSource() {
    this.resolveDestroy();
    for (const listener of this.listeners) listener();
  }
  onDestroy() {
    return this.destroyPromise;
  }
}
type Seat = {
  selectionDataSource?: DataSource;
  selectionListeners: Array<() => void>;
  setSelectionInternal(source: unknown, serial: number): void;
  notifyKeyboardFocusIn(): void;
  notifyKeyboardFocusOut(): void;
  notifyKey(event: Key): void;
  notifyMotion(event: Button): void;
  notifyButton(event: Button): void;
  notifyAxis(event: Axis): void;
  notifyFrame(): void;
  pointer: { buttonCount: number };
};
type InternalSession = {
  renderer: { initScene(sceneId: string, canvas: HTMLCanvasElement): void; resetCursor(): void };
  globals: { register(): void; seat: Seat };
  display: { nextEventSerial(): number };
  flush(): void;
};

export interface GreenfieldBridge {
  copyFromApp(): Promise<string>;
  pasteIntoApp(text: string): void;
  commitComposition(text: string): void;
  setControlling(controlling: boolean): void;
  dispose(): void;
}

export interface GreenfieldInputOptions {
  canControl: () => boolean;
  onCopyShortcut: () => void;
  onPasteShortcut: () => void;
  onCompositionCommitted: (result: "sent" | "too_large" | "unavailable") => void;
  onConnectionStateChange?: (state: "open" | "closed" | "error" | "terminated") => void;
  maxClipboardBytes?: number;
}
type BoundInputOptions = GreenfieldInputOptions & {
  mapKeyEvent: (event: KeyboardEvent, pressed: boolean) => Key | undefined;
};

function buttonEvent(
  event: MouseEvent,
  released: boolean,
  sceneId: string,
  canvas: HTMLCanvasElement,
): Button {
  // The canvas can be CSS-scaled or letterboxed. Wayland coordinates must use
  // its backing pixel size, including DPR 2 displays.
  const bounds = canvas.getBoundingClientRect();
  const scale = Math.min(bounds.width / canvas.width, bounds.height / canvas.height);
  const paintedWidth = canvas.width * scale;
  const paintedHeight = canvas.height * scale;
  const left = bounds.left + (bounds.width - paintedWidth) / 2;
  const top = bounds.top + (bounds.height - paintedHeight) / 2;
  return {
    x: scale > 0 ? Math.max(0, Math.min(canvas.width, (event.clientX - left) / scale)) : 0,
    y: scale > 0 ? Math.max(0, Math.min(canvas.height, (event.clientY - top) / scale)) : 0,
    timestamp: event.timeStamp,
    buttonCode: event.button,
    released,
    buttons: event.buttons,
    sceneId,
  };
}

function axisEvent(event: WheelEvent, sceneId: string): Axis {
  return {
    deltaMode: event.deltaMode,
    DOM_DELTA_LINE: event.DOM_DELTA_LINE,
    DOM_DELTA_PAGE: event.DOM_DELTA_PAGE,
    DOM_DELTA_PIXEL: event.DOM_DELTA_PIXEL,
    deltaX: event.deltaX,
    deltaY: event.deltaY,
    timestamp: event.timeStamp,
    sceneId,
  };
}

export function bindGreenfieldInput(
  session: InternalSession,
  canvas: HTMLCanvasElement,
  input: HTMLTextAreaElement,
  options: BoundInputOptions,
): GreenfieldBridge {
  const sceneId = canvas.id || "greenfield-output";
  canvas.id = sceneId;
  session.renderer.initScene(sceneId, canvas);
  canvas.style.userSelect = "none";
  const seat = session.globals.seat;
  const pressed = new Map<string, Key>();
  const pointerButtons = new Map<number, Button>();
  const maxBytes = options.maxClipboardBytes ?? MAX_GREENFIELD_CLIPBOARD_BYTES;
  let composing = false;
  let disposed = false;
  let controlling = options.canControl();

  const active = () => !disposed && controlling && options.canControl();
  const sendKey = (code: string, key: string, down: boolean, shiftKey = false) => {
    const event = new KeyboardEvent(down ? "keydown" : "keyup", {
      code,
      key,
      shiftKey,
      bubbles: false,
    });
    const mapped = options.mapKeyEvent(event, down);
    if (!mapped) return;
    seat.notifyKey(mapped);
    if (down) pressed.set(code, mapped);
    else pressed.delete(code);
    session.flush();
  };
  const sendShortcut = (code: string, shiftKey = false) => {
    const heldControl = pressed.has("ControlLeft") || pressed.has("ControlRight");
    if (!heldControl) sendKey("ControlLeft", "Control", true);
    sendKey(code, code.startsWith("Key") ? code.slice(3).toLowerCase() : code, true, shiftKey);
    sendKey(code, code.startsWith("Key") ? code.slice(3).toLowerCase() : code, false, shiftKey);
    if (!heldControl) sendKey("ControlLeft", "Control", false);
  };
  const releaseAll = () => {
    for (const [code, down] of pressed) {
      seat.notifyKey({ ...down, pressed: false, timeStamp: performance.now() });
      pressed.delete(code);
    }
    for (const [button, down] of pointerButtons) {
      seat.notifyButton({ ...down, released: true, timestamp: performance.now() });
      pointerButtons.delete(button);
    }
    seat.notifyFrame();
    session.flush();
    seat.notifyKeyboardFocusOut();
    seat.pointer.buttonCount = 0;
    session.renderer.resetCursor();
    session.flush();
  };
  const focusInput = () => {
    if (!active()) return;
    input.focus({ preventScroll: true });
    seat.notifyKeyboardFocusIn();
    session.flush();
  };
  const onPointerDown = (event: PointerEvent) => {
    event.preventDefault();
    if (!active()) return;
    focusInput();
    canvas.setPointerCapture(event.pointerId);
    const button = buttonEvent(event, false, sceneId, canvas);
    pointerButtons.set(event.button, button);
    seat.notifyMotion(button);
    seat.notifyButton(button);
    seat.notifyFrame();
    session.flush();
  };
  const onPointerUp = (event: PointerEvent) => {
    if (!active()) return;
    if (canvas.hasPointerCapture(event.pointerId)) canvas.releasePointerCapture(event.pointerId);
    const button = buttonEvent(event, true, sceneId, canvas);
    pointerButtons.delete(event.button);
    seat.notifyButton(button);
    seat.notifyFrame();
    session.flush();
  };
  const onPointerMove = (event: PointerEvent) => {
    if (!active()) return;
    const events = event.getCoalescedEvents();
    for (const point of [...events, event]) {
      seat.notifyMotion(buttonEvent(point, false, sceneId, canvas));
    }
    seat.notifyFrame();
    session.flush();
  };
  const onWheel = (event: WheelEvent) => {
    if (!active()) return;
    event.preventDefault();
    seat.notifyAxis(axisEvent(event, sceneId));
    seat.notifyFrame();
    session.flush();
  };
  const onKeyDown = (event: KeyboardEvent) => {
    if (!active()) {
      event.preventDefault();
      return;
    }
    if (composing || event.isComposing || event.key === "Process" || event.key === "Dead") return;
    if (event.code === "MetaLeft" || event.code === "MetaRight") {
      event.preventDefault();
      return;
    }
    if (event.metaKey && !/^(ShiftLeft|ShiftRight|AltLeft|AltRight)$/.test(event.code)) {
      event.preventDefault();
      if (event.repeat) return;
      if (event.code === "KeyC") {
        options.onCopyShortcut();
        return;
      }
      if (event.code === "KeyV") {
        options.onPasteShortcut();
        return;
      }
      if (/^(Key[A-Z]|Digit[0-9])$/.test(event.code)) sendShortcut(event.code, event.shiftKey);
      return;
    }
    const mapped = options.mapKeyEvent(event, true);
    if (!mapped) return;
    event.preventDefault();
    // Browser key repeats are generated by Wayland/XKB while held.
    if (pressed.has(event.code)) return;
    pressed.set(event.code, mapped);
    seat.notifyKey(mapped);
    session.flush();
  };
  const onKeyUp = (event: KeyboardEvent) => {
    if (event.code === "MetaLeft" || event.code === "MetaRight") {
      event.preventDefault();
      return;
    }
    const down = pressed.get(event.code);
    if (!down) return;
    event.preventDefault();
    pressed.delete(event.code);
    seat.notifyKey({ ...down, pressed: false, timeStamp: event.timeStamp });
    session.flush();
  };
  const onCompositionStart = () => {
    composing = true;
  };
  const onCompositionEnd = (event: CompositionEvent) => {
    composing = false;
    input.value = "";
    if (!event.data || !active()) return;
    try {
      setTextSelection(event.data);
      sendShortcut("KeyV");
      options.onCompositionCommitted("sent");
    } catch (error) {
      options.onCompositionCommitted(error instanceof RangeError ? "too_large" : "unavailable");
    }
  };
  const onInput = () => {
    if (!composing) input.value = "";
  };
  const onBlur = () => {
    releaseAll();
  };
  const onPointerCancel = (event: PointerEvent) => {
    const down = pointerButtons.get(event.button);
    if (!down) return;
    pointerButtons.delete(event.button);
    seat.notifyButton({ ...down, released: true, timestamp: performance.now() });
    seat.notifyFrame();
    session.flush();
  };
  const onContextMenu = (event: MouseEvent) => {
    event.preventDefault();
  };

  const setTextSelection = (text: string) => {
    if (!active()) throw new Error("controller unavailable");
    if (new TextEncoder().encode(text).byteLength > maxBytes) {
      throw new RangeError("clipboard text is too large");
    }
    seat.setSelectionInternal(new BrowserTextSource(text), session.display.nextEventSerial());
    session.flush();
  };
  canvas.addEventListener("pointerdown", onPointerDown);
  canvas.addEventListener("pointerup", onPointerUp);
  canvas.addEventListener("pointermove", onPointerMove);
  canvas.addEventListener("pointercancel", onPointerCancel);
  canvas.addEventListener("wheel", onWheel, { passive: false });
  canvas.addEventListener("contextmenu", onContextMenu);
  input.addEventListener("keydown", onKeyDown);
  input.addEventListener("keyup", onKeyUp);
  input.addEventListener("compositionstart", onCompositionStart);
  input.addEventListener("compositionend", onCompositionEnd);
  input.addEventListener("input", onInput);
  input.addEventListener("blur", onBlur);
  window.addEventListener("blur", onBlur);

  return {
    async copyFromApp() {
      if (!active()) throw new Error("controller unavailable");
      focusInput();
      const previous = seat.selectionDataSource;
      const changed = waitForSelection(seat, previous, 2000);
      sendShortcut("KeyC");
      const source = await changed;
      return readTextSelection(source, maxBytes);
    },
    pasteIntoApp(text) {
      setTextSelection(text);
      focusInput();
      sendShortcut("KeyV");
    },
    commitComposition(text) {
      setTextSelection(text);
      focusInput();
      sendShortcut("KeyV");
    },
    setControlling(value) {
      if (controlling && !value) releaseAll();
      controlling = value;
    },
    dispose() {
      if (disposed) return;
      releaseAll();
      disposed = true;
      canvas.removeEventListener("pointerdown", onPointerDown);
      canvas.removeEventListener("pointerup", onPointerUp);
      canvas.removeEventListener("pointermove", onPointerMove);
      canvas.removeEventListener("pointercancel", onPointerCancel);
      canvas.removeEventListener("wheel", onWheel);
      canvas.removeEventListener("contextmenu", onContextMenu);
      input.removeEventListener("keydown", onKeyDown);
      input.removeEventListener("keyup", onKeyUp);
      input.removeEventListener("compositionstart", onCompositionStart);
      input.removeEventListener("compositionend", onCompositionEnd);
      input.removeEventListener("input", onInput);
      input.removeEventListener("blur", onBlur);
      window.removeEventListener("blur", onBlur);
    },
  };
}

function waitForSelection(seat: Seat, previous: DataSource | undefined, timeoutMs: number) {
  return new Promise<DataSource>((resolve, reject) => {
    const listener = () => {
      const source = seat.selectionDataSource;
      if (!source || source === previous) return;
      clearTimeout(timer);
      seat.selectionListeners = seat.selectionListeners.filter(
        (candidate) => candidate !== listener,
      );
      resolve(source);
    };
    const timer = setTimeout(() => {
      seat.selectionListeners = seat.selectionListeners.filter(
        (candidate) => candidate !== listener,
      );
      reject(new Error("application did not provide a new selection"));
    }, timeoutMs);
    seat.selectionListeners.push(listener);
  });
}

async function readTextSelection(source: DataSource, maxBytes: number): Promise<string> {
  const mimeType =
    source.mimeTypes.find((type) => type === "text/plain;charset=utf-8") ??
    source.mimeTypes.find((type) => type === "text/plain");
  if (!mimeType) throw new Error("application selection is not plain text");
  const [readFD, writeFD] = await source.inputOutput.mkfifo();
  source.send(mimeType, writeFD);
  const blob = await Promise.race([
    readFD.readBlob(),
    new Promise<never>((_, reject) =>
      setTimeout(() => {
        reject(new Error("selection timed out"));
      }, 3000),
    ),
  ]);
  if (blob.size > maxBytes) throw new RangeError("clipboard text is too large");
  return new TextDecoder("utf-8", { fatal: true }).decode(await blob.arrayBuffer());
}

export async function attachGreenfieldCompositor(input: {
  canvas: HTMLCanvasElement;
  textarea: HTMLTextAreaElement;
  launchPath: string;
  compositorSessionId: string;
  options: GreenfieldInputOptions;
}): Promise<GreenfieldBridge> {
  const compositor = await import("@gfld/compositor");
  await compositor.initWasm();
  const session = await compositor.createCompositorSession(input.compositorSessionId);
  const bridge = bindGreenfieldInput(
    session as unknown as InternalSession,
    input.canvas,
    input.textarea,
    { ...input.options, mapKeyEvent: compositor.createKeyEventFromKeyboardEvent },
  );
  session.globals.register();
  const launcher = compositor.createAppLauncher(session, "remote");
  const app = launcher.launch(new URL(input.launchPath, window.location.origin), () => undefined);
  const disconnect = () => {
    bridge.dispose();
    // AppContext.close() means SIGTERM. Closing only the signaling transport
    // detaches this view and leaves the supervised Code process alive.
    const signaling = app as unknown as {
      signalingWebSocket?: { close(code?: number, reason?: string): void };
    };
    signaling.signalingWebSocket?.close(1001, "view detached");
  };
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error("Greenfield signaling timed out"));
    }, 10000);
    app.onStateChange = (state) => {
      input.options.onConnectionStateChange?.(state);
      bridge.setControlling(state === "open" && input.options.canControl());
      if (state === "open") {
        clearTimeout(timer);
        resolve();
      } else if (state === "error" || state === "terminated") {
        clearTimeout(timer);
        reject(new Error(`Greenfield signaling ${state}`));
      }
    };
  }).catch((error: unknown) => {
    disconnect();
    throw error;
  });
  return { ...bridge, dispose: disconnect };
}
