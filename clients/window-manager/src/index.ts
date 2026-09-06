export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export type WindowMode =
  | "normal"
  | "minimized"
  | "maximized"
  | "fullscreen"
  | "snap-left"
  | "snap-right";

// The discriminated window kind: Agent Center windows render the task
// composer; app-surface windows render one sandboxed installed-app surface;
// artifact-center/artifact-viewer windows render read-only project reviews.
export type WindowKind =
  | "agent-center"
  | "app-surface"
  | "system-monitor"
  | "device-center"
  | "artifact-center"
  | "artifact-viewer"
  | "knowledge-center"
  | "notification-center"
  | "mission-control"
  | "home"
  | "files"
  | "docs"
  | "code"
  | "browser";

// AppSurfaceRef binds a window to one durable surface session. The URL is
// the same-origin relative path returned by CreateSurface — never a private
// Core/Runtime address.
export interface AppSurfaceRef {
  surfaceSessionId: string;
  url: string;
  projectId: string;
  /** The negotiated renderer id ("web-bundle" | "declarative" | ...). */
  renderer?: string | undefined;
}

// ArtifactRef binds one viewer window to exactly one artifact of one
// project. Viewer windows key on the artifact id, so re-opening the same
// artifact focuses the existing window instead of duplicating it.
export interface ArtifactRef {
  artifactId: string;
  projectId: string;
}

export interface WorkOSWindow {
  id: string;
  appId: string;
  title: string;
  kind: WindowKind;
  surface?: AppSurfaceRef | undefined;
  artifact?: ArtifactRef | undefined;
  rect: Rect;
  restoreRect: Rect;
  mode: WindowMode;
  zIndex: number;
  minimizedMode?: Exclude<WindowMode, "minimized"> | undefined;
}

export interface WindowState {
  windows: WorkOSWindow[];
  nextZIndex: number;
}

export type WindowAction =
  | { type: "open"; window: Omit<WorkOSWindow, "zIndex" | "restoreRect"> }
  | { type: "focus"; id: string }
  | { type: "move"; id: string; x: number; y: number }
  | { type: "resize"; id: string; width: number; height: number }
  | { type: "mode"; id: string; mode: WindowMode; viewport: Rect }
  | { type: "work-area"; viewport: Rect }
  | { type: "snap"; id: string; side: "left" | "right"; viewport: Rect }
  | { type: "close"; id: string }
  | { type: "rename"; id: string; title: string };

export const initialWindowState: WindowState = { windows: [], nextZIndex: 1 };

export function fitRect(rect: Rect, bounds: Rect): Rect {
  const width = Math.min(bounds.width, Math.max(320, rect.width));
  const height = Math.min(bounds.height, Math.max(220, rect.height));
  return {
    x: Math.min(bounds.x + bounds.width - width, Math.max(bounds.x, rect.x)),
    y: Math.min(bounds.y + bounds.height - height, Math.max(bounds.y, rect.y)),
    width,
    height,
  };
}

function modeRect(mode: WindowMode, rect: Rect, viewport: Rect): Rect {
  if (mode === "maximized" || mode === "fullscreen") return { ...viewport };
  if (mode === "snap-left" || mode === "snap-right") {
    const leftWidth = Math.floor(viewport.width / 2);
    return {
      x: viewport.x + (mode === "snap-right" ? leftWidth : 0),
      y: viewport.y,
      width: mode === "snap-right" ? viewport.width - leftWidth : leftWidth,
      height: viewport.height,
    };
  }
  return fitRect(rect, viewport);
}

function restoreMinimized(item: WorkOSWindow): WorkOSWindow {
  return item.mode === "minimized" ? { ...item, mode: item.minimizedMode ?? "normal" } : item;
}

export function windowReducer(state: WindowState, action: WindowAction): WindowState {
  if (action.type === "work-area") {
    return {
      ...state,
      windows: state.windows.map((item) => ({
        ...item,
        rect: modeRect(
          item.mode === "minimized" ? (item.minimizedMode ?? "normal") : item.mode,
          item.rect,
          action.viewport,
        ),
        restoreRect: fitRect(item.restoreRect, action.viewport),
      })),
    };
  }
  if (action.type === "open") {
    const existing = state.windows.some((item) => item.id === action.window.id);
    return {
      windows: existing
        ? state.windows.map((item) =>
            item.id === action.window.id
              ? { ...restoreMinimized(item), zIndex: state.nextZIndex }
              : item,
          )
        : [
            ...state.windows,
            { ...action.window, restoreRect: action.window.rect, zIndex: state.nextZIndex },
          ],
      nextZIndex: state.nextZIndex + 1,
    };
  }
  if (action.type === "close")
    return { ...state, windows: state.windows.filter((item) => item.id !== action.id) };
  if (!state.windows.some((item) => item.id === action.id)) return state;
  const raises = action.type === "focus" || action.type === "mode" || action.type === "snap";
  const windows = state.windows.map((item) => {
    if (item.id !== action.id) return item;
    switch (action.type) {
      case "rename":
        return { ...item, title: action.title };
      case "focus":
        return { ...restoreMinimized(item), zIndex: state.nextZIndex };
      case "move":
        return item.mode === "normal"
          ? { ...item, rect: { ...item.rect, x: action.x, y: action.y } }
          : item;
      case "resize":
        return item.mode === "normal"
          ? {
              ...item,
              rect: {
                ...item.rect,
                width: Math.max(320, action.width),
                height: Math.max(220, action.height),
              },
            }
          : item;
      case "mode":
      case "snap": {
        const mode: WindowMode =
          action.type === "snap"
            ? action.side === "left"
              ? "snap-left"
              : "snap-right"
            : action.mode;
        const restoreRect = item.mode === "normal" ? item.rect : item.restoreRect;
        return {
          ...item,
          restoreRect,
          mode,
          zIndex: state.nextZIndex,
          minimizedMode:
            mode === "minimized"
              ? item.mode === "minimized"
                ? (item.minimizedMode ?? "normal")
                : item.mode
              : item.minimizedMode,
          rect:
            mode === "minimized"
              ? item.rect
              : modeRect(mode, mode === "normal" ? restoreRect : item.rect, action.viewport),
        };
      }
      default:
        return item;
    }
  });
  return { windows, nextZIndex: raises ? state.nextZIndex + 1 : state.nextZIndex };
}
