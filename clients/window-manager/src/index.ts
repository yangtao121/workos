export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export type WindowMode = "normal" | "minimized" | "maximized" | "fullscreen";

export interface WorkOSWindow {
  id: string;
  appId: string;
  title: string;
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
  | { type: "close"; id: string };

export const initialWindowState: WindowState = { windows: [], nextZIndex: 1 };

export function windowReducer(state: WindowState, action: WindowAction): WindowState {
  if (action.type === "open") {
    if (state.windows.some((item) => item.id === action.window.id)) return state;
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
      default:
        return item;
    }
  });
  const raises = action.type === "focus" || action.type === "mode";
  return { windows, nextZIndex: raises ? state.nextZIndex + 1 : state.nextZIndex };
}
