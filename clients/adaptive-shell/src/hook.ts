import { useEffect, useRef, useState } from "react";
import type { FoldSegment } from "./device.js";
import { resolveDeviceLayout, type DeviceLayout } from "./layout.js";

// The DOM adapter of the shell: it owns every browser API the layout
// derivation consumes (resize, orientation, visual viewport, and the
// Window Segments Management API when the host exposes it) and feeds plain
// values to the pure `resolveDeviceLayout`. Resize storms are coalesced to
// one animation frame and the published state only changes when the derived
// layout actually changes.

interface WindowWithSegments {
  getWindowSegments?: () => DOMRect[];
}

function readSegments(): FoldSegment[] | undefined {
  // Feature-detect, never UA-sniff: a browser without the fold API simply
  // reports no segments and the derivation fails soft.
  const segments = (window as unknown as WindowWithSegments).getWindowSegments;
  if (typeof segments !== "function") return undefined;
  try {
    const rects = segments.call(window);
    return rects.map((rect) => ({ x: rect.x, y: rect.y, width: rect.width, height: rect.height }));
  } catch {
    return undefined;
  }
}

function readLayout(): DeviceLayout {
  const visualViewport = window.visualViewport;
  return resolveDeviceLayout({
    viewportWidth: visualViewport?.width ?? window.innerWidth,
    viewportHeight: visualViewport?.height ?? window.innerHeight,
    pixelRatio: window.devicePixelRatio,
    segments: readSegments(),
  });
}

export function useDeviceLayout(): DeviceLayout {
  const [layout, setLayout] = useState<DeviceLayout>(readLayout);
  const frame = useRef(0);

  useEffect(() => {
    // requestAnimationFrame is missing in bare jsdom environments; a timer
    // fallback keeps the coalescing semantics everywhere.
    const scheduleFrame: (callback: FrameRequestCallback) => number =
      typeof requestAnimationFrame === "function"
        ? requestAnimationFrame.bind(window)
        : (callback: FrameRequestCallback) => {
            return setTimeout(() => {
              callback(performance.now());
            }, 16) as unknown as number;
          };
    const cancelFrame: (handle: number) => void =
      typeof cancelAnimationFrame === "function"
        ? cancelAnimationFrame.bind(window)
        : (handle: number) => {
            clearTimeout(handle);
          };
    const schedule = () => {
      if (frame.current) return;
      frame.current = scheduleFrame(() => {
        frame.current = 0;
        setLayout((current) => {
          const next = readLayout();
          // Identity-stable updates: a resize storm that does not change
          // the derived mode, class, orientation, DPR, or hinge never
          // re-renders the shell.
          return layoutsEqual(current, next) ? current : next;
        });
      });
    };
    window.addEventListener("resize", schedule);
    window.addEventListener("orientationchange", schedule);
    window.visualViewport?.addEventListener("resize", schedule);
    return () => {
      window.removeEventListener("resize", schedule);
      window.removeEventListener("orientationchange", schedule);
      window.visualViewport?.removeEventListener("resize", schedule);
      if (frame.current) cancelFrame(frame.current);
    };
  }, []);

  return layout;
}

export function layoutsEqual(left: DeviceLayout, right: DeviceLayout): boolean {
  return (
    left.mode === right.mode &&
    left.deviceClass === right.deviceClass &&
    left.orientation === right.orientation &&
    left.pixelRatio === right.pixelRatio &&
    left.dualPane === right.dualPane &&
    left.hinge?.horizontal === right.hinge?.horizontal &&
    left.hinge?.gap === right.hinge?.gap &&
    left.segments.length === right.segments.length &&
    left.segments.every((segment, index) => {
      const other = right.segments[index];
      return (
        other !== undefined &&
        segment.x === other.x &&
        segment.y === other.y &&
        segment.width === other.width &&
        segment.height === other.height
      );
    })
  );
}
