import {
  createAppLauncher,
  createCompositorSession,
  createKeyEventFromKeyboardEvent,
  initWasm,
  type CompositorSurface,
} from "@gfld/compositor";
import { webInputOutput } from "@gfld/compositor/dist/web/WebInputOutput.js";

type Rect = { x: number; y: number; width: number; height: number };
type WindowFact = {
  id: string;
  parentWindowId: string;
  title: string;
  appId: string;
  contentRect: Rect;
  visualRect: Rect;
  devicePixelRatioMillis: number;
  zOrder: number;
  active: boolean;
  revision: string;
};
type FrameTile = {
  x: number;
  y: number;
  width: number;
  height: number;
  pngBase64: string;
};
type BrowserMessage =
  | { kind: "windows"; windows: WindowFact[] }
  | {
      kind: "frame";
      windowId: string;
      windowRevision: string;
      frameSequence: string;
      frameWidth: number;
      frameHeight: number;
      fullRefresh: boolean;
      renderedAt: string;
      tiles: FrameTile[];
    }
  | { kind: "failure"; reasonCode: string };
type InputPayload = {
  windowId: string;
  event: {
    case: "pointer" | "key" | "text" | "resize" | "clipboardWrite" | "focus";
    value: Record<string, unknown>;
  };
};
type Config = {
  compositorSessionId: string;
  launchUrl: string;
  width: number;
  height: number;
  devicePixelRatioMillis: number;
};

declare global {
  interface Window {
    workosChildConfig?: Config;
    workosPush: (message: BrowserMessage) => Promise<void>;
    workosChildApplyInput: (input: InputPayload) => void;
    workosChildReadClipboard: () => Promise<string>;
    workosChildForceFrames: () => Promise<void>;
    workosChildSnapshot: () => WindowFact[];
    workosChildReady: boolean;
  }
}

type UpstreamSession = Awaited<ReturnType<typeof createCompositorSession>>;
type Surface = {
  resource: { id: number; client: { id: string } };
  parent?: Surface;
  state: { bufferContents?: unknown };
  role?: {
    window?: { transientFor?: { surface?: Surface } };
    desktopSurface?: {
      role?: { configureSize?: (size: { width: number; height: number }) => void };
    };
  };
};
type View = {
  surface: Surface;
  mapped: boolean;
  regionRect: { position: { x: number; y: number }; size: { width: number; height: number } };
};
type InternalSession = UpstreamSession & {
  renderer: {
    topLevelViews: View[];
    viewStack: View[];
    initScene(sceneId: string, canvas: HTMLCanvasElement): void;
    render(): void;
  };
  display: { nextEventSerial(): number };
  flush(): void;
};
type SelectionSource = {
  mimeTypes: string[];
  inputOutput: {
    mkfifo(): Promise<
      [{ readBlob(): Promise<Blob>; close(): Promise<void> }, { close(): Promise<void> }]
    >;
  };
  send(mimeType: string, fd: unknown): void;
};
type Seat = {
  selectionDataSource?: SelectionSource;
  setSelectionInternal(source: unknown, serial: number): void;
  notifyKey(event: unknown): void;
  notifyKeyboardFocusIn(): void;
  notifyMotion(event: unknown): void;
  notifyButton(event: unknown): void;
  notifyAxis(event: unknown): void;
  notifyFrame(): void;
};
type WindowRecord = {
  id: string;
  surface: CompositorSurface;
  sceneId: string;
  canvas: HTMLCanvasElement;
  captureCanvas: HTMLCanvasElement;
  title: string;
  appId: string;
  active: boolean;
  parentWindowId: string;
  contentRect: Rect;
  visualRect: Rect;
  pixelRatio: number;
  scenePixelRatio: number;
  zOrder: number;
  revision: bigint;
  frameSequence: bigint;
  previousPixels?: Uint8ClampedArray;
  lastFullAt: number;
  capturePending: boolean;
  captureAgain: boolean;
  forceFull: boolean;
  readyForCapture: boolean;
  firstNativeAt: number;
};

const textEncoder = new TextEncoder();
const MAX_CLIPBOARD = 1024 * 1024;
const MAX_WINDOWS = 16;
const MAX_TILE_BYTES = 2 * 1024 * 1024;
const MAX_FRAME_BYTES = 24 * 1024 * 1024;
const TILE_SIZE = 512;
const emptyRect: Rect = { x: 0, y: 0, width: 0, height: 0 };

function uuidv7(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  const now = BigInt(Date.now());
  for (let index = 0; index < 6; index++)
    bytes[5 - index] = Number((now >> BigInt(index * 8)) & 255n);
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function keyOf(surface: CompositorSurface): string {
  return `${surface.client.id}:${String(surface.id)}`;
}

function sameSurface(view: View, surface: CompositorSurface): boolean {
  return (
    view.surface.resource.id === surface.id && view.surface.resource.client.id === surface.client.id
  );
}

function nativeSurfaceKey(surface: Surface): string {
  return `${surface.resource.client.id}:${String(surface.resource.id)}`;
}

function parentWindowId(view: View, windows: Map<string, WindowRecord>): string {
  // XDG/Wayland parents are represented by Surface.parent. XWayland's
  // WM_TRANSIENT_FOR can also remain on XWindow.transientFor when the dialog is
  // itself a top-level view, so inspect both fixed rc1 relationships.
  let parent = view.surface.parent ?? view.surface.role?.window?.transientFor?.surface;
  for (let depth = 0; parent && depth < 64; depth++) {
    const record = windows.get(nativeSurfaceKey(parent));
    if (record) return record.id;
    parent = parent.parent ?? parent.role?.window?.transientFor?.surface;
  }
  return "";
}

function belongsTo(surface: Surface, root: Surface): boolean {
  let parent: Surface | undefined = surface;
  for (let depth = 0; parent && depth < 64; depth++) {
    if (parent === root) return true;
    parent = parent.parent;
  }
  return false;
}

function viewRect(view: View): Rect | undefined {
  const rect = view.regionRect;
  const x = Math.floor(rect.position.x);
  const y = Math.floor(rect.position.y);
  const width = Math.ceil(rect.size.width);
  const height = Math.ceil(rect.size.height);
  if (![x, y, width, height].every(Number.isFinite) || width <= 0 || height <= 0) return;
  return { x, y, width, height };
}

function unionRect(rects: Rect[]): Rect {
  const x = Math.min(...rects.map((rect) => rect.x));
  const y = Math.min(...rects.map((rect) => rect.y));
  const right = Math.max(...rects.map((rect) => rect.x + rect.width));
  const bottom = Math.max(...rects.map((rect) => rect.y + rect.height));
  return { x, y, width: right - x, height: bottom - y };
}

function sameRect(left: Rect, right: Rect): boolean {
  return (
    left.x === right.x &&
    left.y === right.y &&
    left.width === right.width &&
    left.height === right.height
  );
}

function tileChanged(
  current: Uint8ClampedArray,
  previous: Uint8ClampedArray | undefined,
  frameWidth: number,
  x: number,
  y: number,
  width: number,
  height: number,
): boolean {
  if (!previous || previous.length !== current.length) return true;
  for (let row = y; row < y + height; row++) {
    const start = (row * frameWidth + x) * 4;
    const end = start + width * 4;
    for (let index = start; index < end; index++) {
      if (current[index] !== previous[index]) return true;
    }
  }
  return false;
}

function copyTile(
  current: Uint8ClampedArray,
  previous: Uint8ClampedArray,
  frameWidth: number,
  x: number,
  y: number,
  width: number,
  height: number,
): void {
  for (let row = y; row < y + height; row++) {
    const start = (row * frameWidth + x) * 4;
    previous.set(current.subarray(start, start + width * 4), start);
  }
}

function hasSubstantiveContent(pixels: Uint8ClampedArray, width: number, height: number): boolean {
  const colorBuckets = new Map<number, number>();
  let sampled = 0;
  const left = Math.floor(width * 0.15);
  const right = Math.ceil(width * 0.85);
  const top = Math.floor(height * 0.15);
  const bottom = Math.ceil(height * 0.85);
  for (let y = top; y < bottom; y += 8) {
    for (let x = left; x < right; x += 8) {
      const index = (y * width + x) * 4;
      if (pixels[index + 3] === 0) continue;
      const bucket =
        ((pixels[index] >> 4) << 8) | ((pixels[index + 1] >> 4) << 4) | (pixels[index + 2] >> 4);
      colorBuckets.set(bucket, (colorBuckets.get(bucket) ?? 0) + 1);
      sampled++;
    }
  }
  const dominant = Math.max(0, ...colorBuckets.values());
  return sampled > 0 && colorBuckets.size >= 8 && sampled - dominant >= sampled * 0.01;
}

async function pngBase64(canvas: HTMLCanvasElement): Promise<{ base64: string; size: number }> {
  const blob = await new Promise<Blob>((resolve, reject) => {
    canvas.toBlob((result) => {
      if (result) resolve(result);
      else reject(new Error("PNG_ENCODE_FAILED"));
    }, "image/png");
  });
  if (blob.size > MAX_TILE_BYTES) throw new Error("TILE_TOO_LARGE");
  const dataUrl = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => {
      reject(new Error("PNG_READ_FAILED"));
    };
    reader.onload = () => {
      if (typeof reader.result === "string") resolve(reader.result);
      else reject(new Error("PNG_READ_FAILED"));
    };
    reader.readAsDataURL(blob);
  });
  return { base64: dataUrl.slice(dataUrl.indexOf(",") + 1), size: blob.size };
}

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
  private destroyed!: () => void;
  private destroyPromise = new Promise<void>((resolve) => {
    this.destroyed = resolve;
  });
  constructor(private readonly text: string) {}
  send(mimeType: string, fd: { write(data: Blob): Promise<void>; close(): Promise<void> }) {
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
    this.listeners = this.listeners.filter((item) => item !== listener);
  }
  destroyDataSource() {
    this.destroyed();
    for (const listener of this.listeners) listener();
  }
  onDestroy() {
    return this.destroyPromise;
  }
}

async function readSelection(source: SelectionSource): Promise<string> {
  const mime =
    source.mimeTypes.find((type) => type === "text/plain;charset=utf-8") ??
    source.mimeTypes.find((type) => type === "text/plain");
  if (!mime) throw new Error("NO_TEXT_SELECTION");
  const [readFD, writeFD] = await source.inputOutput.mkfifo();
  source.send(mime, writeFD);
  const blob = await Promise.race([
    readFD.readBlob(),
    new Promise<never>((_, reject) =>
      setTimeout(() => {
        reject(new Error("SELECTION_TIMEOUT"));
      }, 3000),
    ),
  ]);
  try {
    if (blob.size > MAX_CLIPBOARD) throw new Error("SELECTION_TOO_LARGE");
    return new TextDecoder("utf-8", { fatal: true }).decode(await blob.arrayBuffer());
  } finally {
    await readFD.close();
  }
}

async function main(): Promise<void> {
  const config = window.workosChildConfig;
  if (!config || !Reflect.has(window, "workosPush")) throw new Error("CHILD_CONFIG_MISSING");
  await initWasm();
  const session = (await createCompositorSession(config.compositorSessionId)) as InternalSession;
  const seat = session.globals.seat as unknown as Seat;
  const windows = new Map<string, WindowRecord>();
  const pendingMeta = new Map<string, { title?: string; appId?: string; active?: boolean }>();
  const display = document.querySelector<HTMLCanvasElement>("#display");
  const windowContainer = document.querySelector<HTMLElement>("#windows");
  if (!display || !windowContainer) throw new Error("CHILD_CANVAS_MISSING");
  display.width = config.width;
  display.height = config.height;
  display.style.width = `${String(config.width)}px`;
  display.style.height = `${String(config.height)}px`;
  session.renderer.initScene("workos-display", display);

  const facts = (): WindowFact[] =>
    [...windows.values()]
      .filter(
        (record) =>
          record.readyForCapture && record.visualRect.width > 0 && record.visualRect.height > 0,
      )
      .map((record) => ({
        id: record.id,
        parentWindowId: record.parentWindowId,
        title: record.title,
        appId: record.appId,
        contentRect: record.contentRect,
        visualRect: record.visualRect,
        devicePixelRatioMillis: Math.round(record.scenePixelRatio * 1000),
        zOrder: record.zOrder,
        active: record.active,
        revision: record.revision.toString(),
      }));
  const publishWindows = () => void window.workosPush({ kind: "windows", windows: facts() });
  const fail = (reasonCode: string) => void window.workosPush({ kind: "failure", reasonCode });

  const refreshGeometry = (record: WindowRecord): void => {
    const topIndex = session.renderer.topLevelViews.findIndex((view) =>
      sameSurface(view, record.surface),
    );
    if (topIndex < 0) return;
    const top = session.renderer.topLevelViews[topIndex];
    if (!top.mapped) return;
    const content = viewRect(top);
    if (!content) return;
    const stack = (session.renderer as unknown as { viewStack: View[] }).viewStack;
    const rects = stack
      .filter((view) => view.mapped && belongsTo(view.surface, top.surface))
      .map(viewRect)
      .filter((rect): rect is Rect => rect !== undefined);
    const visual = rects.length ? unionRect(rects) : content;
    const physicalWidth = Math.round(visual.width * record.pixelRatio);
    const physicalHeight = Math.round(visual.height * record.pixelRatio);
    if (
      physicalWidth < 1 ||
      physicalHeight < 1 ||
      physicalWidth > 4096 ||
      physicalHeight > 4096 ||
      Math.ceil(physicalWidth / TILE_SIZE) * Math.ceil(physicalHeight / TILE_SIZE) > 64
    ) {
      fail("WINDOW_DIMENSIONS_EXCEEDED");
      return;
    }
    const ready = Boolean(top.surface.state.bufferContents);
    const parentId = parentWindowId(top, windows);
    const changed =
      !sameRect(content, record.contentRect) ||
      !sameRect(visual, record.visualRect) ||
      topIndex !== record.zOrder ||
      parentId !== record.parentWindowId ||
      record.pixelRatio !== record.scenePixelRatio ||
      ready !== record.readyForCapture;
    if (ready && !record.readyForCapture) record.firstNativeAt = Date.now();
    record.readyForCapture = ready;
    if (!changed) return;
    record.contentRect = content;
    record.visualRect = visual;
    record.zOrder = topIndex;
    record.parentWindowId = parentId;
    record.scenePixelRatio = record.pixelRatio;
    record.revision++;
    record.forceFull = true;
    record.previousPixels = undefined;
    session.userShell.actions.updateWindowScene(record.sceneId, visual, record.pixelRatio);
    publishWindows();
  };

  const capture = async (record: WindowRecord): Promise<void> => {
    const hasCaptureAgain = () => record.captureAgain;
    if (record.capturePending) {
      record.captureAgain = true;
      return;
    }
    if (!record.readyForCapture || !record.visualRect.width || !record.visualRect.height) return;
    record.capturePending = true;
    try {
      do {
        record.captureAgain = false;
        const width = record.canvas.width;
        const height = record.canvas.height;
        if (!width || !height || width > 4096 || height > 4096) break;
        record.captureCanvas.width = width;
        record.captureCanvas.height = height;
        const context = record.captureCanvas.getContext("2d", { willReadFrequently: true });
        if (!context) throw new Error("READBACK_UNAVAILABLE");
        context.drawImage(record.canvas, 0, 0);
        const pixels = context.getImageData(0, 0, width, height).data;
        // A newly created scene may be resized before its first native buffer
        // is drawn. Transparent pixels are not a valid first-frame readiness
        // signal even though the compositor already knows the window geometry.
        let hasNativePixel = false;
        for (let index = 3; index < pixels.length; index += 4) {
          if (pixels[index] !== 0) {
            hasNativePixel = true;
            break;
          }
        }
        if (!hasNativePixel) break;
        const now = Date.now();
        // Native clients can commit an opaque black startup buffer before
        // their UI paints. Delay the first frame until the center has visible
        // detail, with a four-second fallback for intentionally blank apps.
        if (
          record.frameSequence === 0n &&
          now - record.firstNativeAt < 4000 &&
          !hasSubstantiveContent(pixels, width, height)
        )
          break;
        const fullRefresh =
          record.forceFull ||
          now - record.lastFullAt >= 1000 ||
          !record.previousPixels ||
          record.previousPixels.length !== pixels.length;
        const changed: Array<{ x: number; y: number; width: number; height: number }> = [];
        for (let y = 0; y < height; y += TILE_SIZE) {
          for (let x = 0; x < width; x += TILE_SIZE) {
            const tileWidth = Math.min(TILE_SIZE, width - x);
            const tileHeight = Math.min(TILE_SIZE, height - y);
            if (
              fullRefresh ||
              tileChanged(pixels, record.previousPixels, width, x, y, tileWidth, tileHeight)
            ) {
              changed.push({ x, y, width: tileWidth, height: tileHeight });
            }
          }
        }
        if (changed.length === 0) break;
        if (changed.length > 64) throw new Error("FRAME_TILE_LIMIT");
        const tiles: FrameTile[] = [];
        let totalBytes = 0;
        for (const tile of changed) {
          const tileCanvas = document.createElement("canvas");
          tileCanvas.width = tile.width;
          tileCanvas.height = tile.height;
          const tileContext = tileCanvas.getContext("2d");
          if (!tileContext) throw new Error("PNG_CONTEXT_UNAVAILABLE");
          tileContext.drawImage(
            record.captureCanvas,
            tile.x,
            tile.y,
            tile.width,
            tile.height,
            0,
            0,
            tile.width,
            tile.height,
          );
          const encoded = await pngBase64(tileCanvas);
          totalBytes += encoded.size;
          if (totalBytes > MAX_FRAME_BYTES) throw new Error("FRAME_TOO_LARGE");
          tiles.push({ ...tile, pngBase64: encoded.base64 });
        }
        record.frameSequence++;
        await window.workosPush({
          kind: "frame",
          windowId: record.id,
          windowRevision: record.revision.toString(),
          frameSequence: record.frameSequence.toString(),
          frameWidth: width,
          frameHeight: height,
          fullRefresh,
          renderedAt: new Date(now).toISOString(),
          tiles,
        });
        if (!record.previousPixels || record.previousPixels.length !== pixels.length) {
          record.previousPixels = new Uint8ClampedArray(pixels.length);
        }
        for (const tile of changed) {
          copyTile(pixels, record.previousPixels, width, tile.x, tile.y, tile.width, tile.height);
        }
        if (fullRefresh) record.lastFullAt = now;
        record.forceFull = false;
      } while (hasCaptureAgain());
    } catch (error) {
      fail(error instanceof Error ? error.message : "FRAME_CAPTURE_FAILED");
    } finally {
      record.capturePending = false;
    }
  };

  session.userShell.events.surfaceCreated = (surface) => {
    if (windows.size >= MAX_WINDOWS) {
      fail("WINDOW_LIMIT_EXCEEDED");
      return;
    }
    const id = uuidv7();
    const canvas = document.createElement("canvas");
    canvas.id = `window-${id}`;
    canvas.style.width = "1px";
    canvas.style.height = "1px";
    windowContainer.appendChild(canvas);
    session.userShell.actions.initWindowScene(id, canvas, surface);
    const meta = pendingMeta.get(keyOf(surface));
    windows.set(keyOf(surface), {
      id,
      surface,
      sceneId: id,
      canvas,
      captureCanvas: document.createElement("canvas"),
      title: meta?.title ?? "",
      appId: meta?.appId ?? "",
      active: meta?.active ?? false,
      parentWindowId: "",
      contentRect: emptyRect,
      visualRect: emptyRect,
      pixelRatio: config.devicePixelRatioMillis / 1000,
      scenePixelRatio: 0,
      zOrder: -1,
      revision: 1n,
      frameSequence: 0n,
      lastFullAt: 0,
      capturePending: false,
      captureAgain: false,
      forceFull: true,
      readyForCapture: false,
      firstNativeAt: 0,
    });
    publishWindows();
    session.renderer.render();
  };
  session.userShell.events.surfaceDestroyed = (surface) => {
    const record = windows.get(keyOf(surface));
    if (!record) return;
    windows.delete(keyOf(surface));
    session.userShell.actions.destroyScene(record.sceneId);
    record.canvas.remove();
    pendingMeta.delete(keyOf(surface));
    publishWindows();
  };
  session.userShell.events.surfaceTitleUpdated = (surface, title) => {
    if (textEncoder.encode(title).length > 4096) {
      fail("WINDOW_TITLE_TOO_LARGE");
      return;
    }
    const key = keyOf(surface);
    const meta = pendingMeta.get(key) ?? {};
    meta.title = title;
    pendingMeta.set(key, meta);
    const record = windows.get(key);
    if (record && record.title !== title) {
      record.title = title;
      record.revision++;
      publishWindows();
    }
  };
  session.userShell.events.surfaceAppIdUpdated = (surface, appId) => {
    if (textEncoder.encode(appId).length > 4096) {
      fail("WINDOW_APP_ID_TOO_LARGE");
      return;
    }
    const key = keyOf(surface);
    const meta = pendingMeta.get(key) ?? {};
    meta.appId = appId;
    pendingMeta.set(key, meta);
    const record = windows.get(key);
    if (record && record.appId !== appId) {
      record.appId = appId;
      record.revision++;
      publishWindows();
    }
  };
  session.userShell.events.surfaceActivationUpdated = (surface, active) => {
    const key = keyOf(surface);
    const meta = pendingMeta.get(key) ?? {};
    meta.active = active;
    pendingMeta.set(key, meta);
    const record = windows.get(key);
    if (record && record.active !== active) {
      record.active = active;
      record.revision++;
      publishWindows();
    }
  };
  session.userShell.events.sceneRefreshed = (sceneId) => {
    if (sceneId === "workos-display") {
      for (const record of windows.values()) refreshGeometry(record);
      return;
    }
    for (const record of windows.values()) {
      if (record.sceneId === sceneId) {
        void capture(record);
        break;
      }
    }
  };

  const findWindow = (id: string): WindowRecord => {
    const found = [...windows.values()].find((record) => record.id === id);
    if (!found) throw new Error("WINDOW_NOT_FOUND");
    return found;
  };
  const sendKey = (
    code: string,
    key: string,
    pressed: boolean,
    modifiers: {
      ctrl?: boolean;
      alt?: boolean;
      meta?: boolean;
      shift?: boolean;
      repeat?: boolean;
    } = {},
  ) => {
    const browserEvent = new KeyboardEvent(pressed ? "keydown" : "keyup", {
      code,
      key,
      ctrlKey: modifiers.ctrl,
      altKey: modifiers.alt,
      metaKey: modifiers.meta,
      shiftKey: modifiers.shift,
      repeat: modifiers.repeat,
    });
    const nativeEvent = createKeyEventFromKeyboardEvent(browserEvent, pressed);
    if (!nativeEvent) throw new Error("KEY_UNSUPPORTED");
    seat.notifyKey(nativeEvent);
    session.flush();
  };
  const setSelection = (text: string) => {
    if (textEncoder.encode(text).length > MAX_CLIPBOARD) throw new Error("SELECTION_TOO_LARGE");
    seat.setSelectionInternal(new BrowserTextSource(text), session.display.nextEventSerial());
    session.flush();
  };
  const pasteText = (text: string) => {
    setSelection(text);
    sendKey("ControlLeft", "Control", true);
    sendKey("KeyV", "v", true, { ctrl: true });
    sendKey("KeyV", "v", false, { ctrl: true });
    sendKey("ControlLeft", "Control", false);
  };

  window.workosChildApplyInput = (input) => {
    const record = findWindow(input.windowId);
    const { event } = input;
    const value = event.value;
    switch (event.case) {
      case "focus":
        session.userShell.actions.activateSurface(record.surface);
        seat.notifyKeyboardFocusIn();
        session.flush();
        return;
      case "pointer": {
        const x = Number(value.x);
        const y = Number(value.y);
        if (!Number.isFinite(x) || !Number.isFinite(y)) throw new Error("POINTER_INVALID");
        const action = Number(value.action);
        if (action === 2) {
          session.userShell.actions.activateSurface(record.surface);
          seat.notifyKeyboardFocusIn();
        }
        const buttonCode = Number(value.button ?? 0);
        const pointer = {
          x: record.visualRect.x + x,
          y: record.visualRect.y + y,
          timestamp: performance.now(),
          buttonCode,
          released: action === 3,
          buttons: action === 2 ? 1 << buttonCode : 0,
          sceneId: "workos-display",
        };
        // Greenfield's browser/input queues only a button event for pointerup.
        // A synthetic motion immediately before release can make an XWayland
        // menu dismiss itself even though the pointer coordinates did not move.
        if (action !== 3) seat.notifyMotion(pointer);
        if (action === 2 || action === 3) seat.notifyButton(pointer);
        if (action === 4)
          seat.notifyAxis({
            deltaMode: 0,
            DOM_DELTA_LINE: 1,
            DOM_DELTA_PAGE: 2,
            DOM_DELTA_PIXEL: 0,
            deltaX: Number(value.deltaX),
            deltaY: Number(value.deltaY),
            timestamp: performance.now(),
            sceneId: "workos-display",
          });
        seat.notifyFrame();
        session.flush();
        return;
      }
      case "key":
        sendKey(String(value.code), String(value.key), Number(value.action) === 1, {
          ctrl: Boolean(value.ctrl),
          alt: Boolean(value.alt),
          meta: Boolean(value.meta),
          shift: Boolean(value.shift),
          repeat: Boolean(value.repeat),
        });
        return;
      case "text":
        pasteText(String(value.text));
        return;
      case "clipboardWrite":
        setSelection(String(value.text));
        return;
      case "resize": {
        const width = Number(value.contentWidth);
        const height = Number(value.contentHeight);
        if (
          !Number.isInteger(width) ||
          !Number.isInteger(height) ||
          width < 1 ||
          height < 1 ||
          width > 4096 ||
          height > 4096
        )
          throw new Error("RESIZE_INVALID");
        const top = session.renderer.topLevelViews.find((view) =>
          sameSurface(view, record.surface),
        );
        const role = top?.surface.role?.desktopSurface?.role;
        if (!role || typeof role.configureSize !== "function")
          throw new Error("RESIZE_UNAVAILABLE");
        role.configureSize({ width, height });
        const ratio = Number(value.devicePixelRatioMillis) / 1000;
        if (Number.isFinite(ratio) && ratio >= 1 && ratio <= 4) record.pixelRatio = ratio;
        session.flush();
        session.renderer.render();
        return;
      }
    }
  };
  window.workosChildReadClipboard = async () => {
    const source = seat.selectionDataSource;
    if (!source) throw new Error("NO_NATIVE_SELECTION");
    return readSelection(source);
  };
  window.workosChildForceFrames = async () => {
    for (const record of windows.values()) {
      record.forceFull = true;
      await capture(record);
    }
  };
  window.workosChildSnapshot = facts;
  setInterval(() => {
    for (const record of windows.values()) {
      if (Date.now() - record.lastFullAt >= 1000) {
        record.forceFull = true;
        void capture(record);
      }
    }
  }, 250);

  session.globals.register();
  const launcher = createAppLauncher(session, "remote");
  const app = launcher.launch(new URL(config.launchUrl), () => undefined);
  app.onStateChange = (state) => {
    if (state === "error" || state === "terminated") fail("GREENFIELD_SIGNALING_FAILED");
    if (state === "open") window.workosChildReady = true;
  };
  publishWindows();
}

void main().catch((error: unknown) => {
  const push = Reflect.get(window, "workosPush") as Window["workosPush"] | undefined;
  if (!push) return;
  void push({
    kind: "failure",
    reasonCode: error instanceof Error ? error.message : "CHILD_COMPOSITOR_FAILED",
  });
});
