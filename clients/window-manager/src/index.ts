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
  | { type: "mode"; id: string; mode: WindowMode }
  | { type: "snap"; id: string; side: "left" | "right"; viewport: Rect }
  | { type: "close"; id: string }
  | { type: "rename"; id: string; title: string };

export const initialWindowState: WindowState = { windows: [], nextZIndex: 1 };

export function windowReducer(state: WindowState, action: WindowAction): WindowState {
  if (action.type === "open") {
    if (state.windows.some((item) => item.id === action.window.id)) {
      return {
        windows: state.windows.map((item) =>
          item.id === action.window.id ? { ...item, zIndex: state.nextZIndex } : item,
        ),
        nextZIndex: state.nextZIndex + 1,
      };
    }
    return {
      windows: [
        ...state.windows,
        { ...action.window, restoreRect: action.window.rect, zIndex: state.nextZIndex },
      ],
      nextZIndex: state.nextZIndex + 1,
    };
  }
  if (action.type === "close") {
    return { ...state, windows: state.windows.filter((item) => item.id !== action.id) };
  }
  const target = state.windows.find((item) => item.id === action.id);
  if (!target) return state;
  const windows = state.windows.map((item) => {
    if (item.id !== action.id) return item;
    switch (action.type) {
      case "rename":
        return { ...item, title: action.title };
      case "focus":
        return { ...item, zIndex: state.nextZIndex };
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
        return {
          ...item,
          restoreRect: item.mode === "normal" ? item.rect : item.restoreRect,
          rect: action.mode === "normal" ? item.restoreRect : item.rect,
          mode: action.mode,
          zIndex: state.nextZIndex,
        };
      case "snap": {
        // Snap is a deterministic half-viewport geometry (ADR W6): the
        // pre-snap rect is preserved as the restore target, so returning to
        // "normal" is exact. Snapping never nests on an already-snapped
        // window — the restore target stays the last normal rect.
        const half = Math.max(320, Math.floor(action.viewport.width / 2));
        const rect: Rect = {
          x: action.side === "left" ? 0 : Math.max(0, action.viewport.width - half),
          y: 0,
          width: half,
          height: Math.max(220, action.viewport.height),
        };
        return {
          ...item,
          restoreRect: item.mode === "normal" ? item.rect : item.restoreRect,
          rect,
          mode: (action.side === "left" ? "snap-left" : "snap-right") as WindowMode,
          zIndex: state.nextZIndex,
        };
      }
      default:
        return item;
    }
  });
  const raises = action.type === "focus" || action.type === "mode";
  return { windows, nextZIndex: raises ? state.nextZIndex + 1 : state.nextZIndex };
}
