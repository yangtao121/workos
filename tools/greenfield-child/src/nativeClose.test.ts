import { describe, expect, it, vi } from "vitest";
import { requestNativeTransientClose } from "./nativeClose.js";

function fixture() {
  const parentClose = vi.fn();
  const childClose = vi.fn();
  const xFlush = vi.fn();
  const flush = vi.fn();
  const parent = {
    surface: {
      resource: { id: 12, client: { id: "same-client" } },
      role: { desktopSurface: { role: { requestClose: parentClose } } },
    },
  };
  const child = {
    surface: {
      resource: { id: 13, client: { id: "same-client" } },
      role: {
        desktopSurface: {
          role: {
            requestClose: childClose,
            window: { deleteWindow: true, wm: { xConnection: { flush: xFlush } } },
          },
        },
      },
    },
  };
  const record = {
    surface: { id: 13, client: { id: "same-client" } },
    parentWindowId: "parent-uuid",
    readyForCapture: true,
  };
  return { parent, child, record, parentClose, childClose, xFlush, flush };
}

describe("native transient close", () => {
  it("requests closure of only the exact current child surface", () => {
    const f = fixture();
    requestNativeTransientClose(f.record, [f.parent, f.child], () => "parent-uuid", f.flush);
    expect(f.childClose).toHaveBeenCalledOnce();
    expect(f.parentClose).not.toHaveBeenCalled();
    expect(f.xFlush).toHaveBeenCalledOnce();
    expect(f.flush).toHaveBeenCalledOnce();
  });

  it("refuses top-level, stale, reparented and unsupported surfaces", () => {
    const f = fixture();
    expect(() =>
      requestNativeTransientClose(
        { ...f.record, parentWindowId: "" },
        [f.parent, f.child],
        () => "",
        f.flush,
      ),
    ).toThrow("WINDOW_CLOSE_UNAVAILABLE");
    expect(() =>
      requestNativeTransientClose(
        { ...f.record, surface: { id: 13, client: { id: "other-client" } } },
        [f.parent, f.child],
        () => "parent-uuid",
        f.flush,
      ),
    ).toThrow("WINDOW_CLOSE_UNAVAILABLE");
    expect(() =>
      requestNativeTransientClose(f.record, [f.parent, f.child], () => "new-parent", f.flush),
    ).toThrow("WINDOW_CLOSE_UNAVAILABLE");
    expect(() =>
      requestNativeTransientClose(
        f.record,
        [f.parent, { surface: { ...f.child.surface, role: {} } }],
        () => "parent-uuid",
        f.flush,
      ),
    ).toThrow("WINDOW_CLOSE_UNAVAILABLE");
    expect(() =>
      requestNativeTransientClose(
        f.record,
        [
          f.parent,
          {
            surface: {
              ...f.child.surface,
              role: {
                desktopSurface: {
                  role: { requestClose: f.childClose, window: { deleteWindow: false } },
                },
              },
            },
          },
        ],
        () => "parent-uuid",
        f.flush,
      ),
    ).toThrow("WINDOW_CLOSE_UNAVAILABLE");
    expect(f.childClose).not.toHaveBeenCalled();
    expect(f.parentClose).not.toHaveBeenCalled();
    expect(f.xFlush).not.toHaveBeenCalled();
    expect(f.flush).not.toHaveBeenCalled();
  });
});
