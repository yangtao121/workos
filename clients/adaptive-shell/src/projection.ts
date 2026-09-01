import type { WindowState, WorkOSWindow } from "@workos/window-manager";

// The adaptive projection over the shared window-manager state. The window
// entities themselves stay plain data (no React, no DOM); these pure helpers
// decide which windows the compact/medium/fold shells render as the single
// main surface and the optional secondary pane. The expanded shell keeps
// rendering every window directly and never consults this projection.

// orderedWindows lists visible windows focused-first (z-index descending).
export function orderedWindows(state: WindowState): WorkOSWindow[] {
  return state.windows
    .filter((window) => window.mode !== "minimized")
    .sort((left, right) => right.zIndex - left.zIndex);
}

// activeWindow picks the single main surface: the caller's preferred window
// when it still exists and is visible, otherwise the focused (topmost)
// window. It is total — an empty shell has no active window and the shell
// renders its home view.
export function activeWindow(state: WindowState, preferredId?: string): WorkOSWindow | undefined {
  const visible = orderedWindows(state);
  if (preferredId) {
    const preferred = visible.find((window) => window.id === preferredId);
    if (preferred) return preferred;
  }
  return visible[0];
}

// secondaryWindow picks the second pane's window for the fold-separated
// dual-pane mode: the next visible window after the active one, or
// undefined when the shell has only one window (the pane then renders its
// empty state; the shell never invents a second surface).
export function secondaryWindow(
  state: WindowState,
  activeId: string | undefined,
): WorkOSWindow | undefined {
  return orderedWindows(state).find((window) => window.id !== activeId);
}
