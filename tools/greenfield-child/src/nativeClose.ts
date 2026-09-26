import type { CompositorSurface } from "@gfld/compositor";

type NativeCloseSurface = {
  resource: { id: number; client: { id: string } };
  role?: {
    desktopSurface?: {
      role?: {
        requestClose?: () => void;
        window?: { deleteWindow?: boolean; wm?: { xConnection?: { flush?: () => void } } };
      };
    };
  };
};

// A compositor surface ID is scoped to its client. A UUID from the Runtime
// snapshot must still identify that exact current top-level in this child.
export function requestNativeTransientClose<TView extends { surface: NativeCloseSurface }>(
  record: {
    surface: CompositorSurface;
    parentWindowId: string;
    readyForCapture: boolean;
  },
  topLevelViews: readonly TView[],
  currentParentWindowId: (view: TView) => string,
  flush: () => void,
): void {
  if (!record.readyForCapture || !record.parentWindowId)
    throw new Error("WINDOW_CLOSE_UNAVAILABLE");
  const view = topLevelViews.find(
    ({ surface }) =>
      surface.resource.id === record.surface.id &&
      surface.resource.client.id === record.surface.client.id,
  );
  if (!view || currentParentWindowId(view) !== record.parentWindowId)
    throw new Error("WINDOW_CLOSE_UNAVAILABLE");
  const role = view.surface.role?.desktopSurface?.role;
  if (typeof role?.requestClose !== "function") throw new Error("WINDOW_CLOSE_UNAVAILABLE");
  // XWayland's fixed rc1 requestClose silently does nothing when the native
  // window omitted WM_DELETE_WINDOW. Surface this as unavailable before ACK.
  if (role.window?.deleteWindow === false) throw new Error("WINDOW_CLOSE_UNAVAILABLE");
  if (role.window && typeof role.window.wm?.xConnection?.flush !== "function")
    throw new Error("WINDOW_CLOSE_UNAVAILABLE");
  role.requestClose();
  // rc1's XWindow.close queues WM_DELETE_WINDOW but does not flush the XCB
  // connection. Wayland session.flush() below flushes a different transport.
  role.window?.wm?.xConnection?.flush?.();
  flush();
}
