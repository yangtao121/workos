import {
  classifyDevice,
  type FoldSegment,
  type LayoutMode,
  type Orientation,
  type UiDeviceClass,
} from "./device.js";

export interface DeviceLayoutInput {
  viewportWidth: number;
  viewportHeight: number;
  // devicePixelRatio as the browser reports it. Zero, negative, NaN, and
  // ±Infinity are tolerated and normalized to 1: a broken DPR must never
  // break the layout derivation.
  pixelRatio?: number | undefined;
  // Raw window segments when the host exposes the Window Segments
  // Management API (or a test injects it). Absent means the host said
  // nothing about folds — the derivation then fails soft to a
  // single-surface layout.
  segments?: readonly FoldSegment[] | undefined;
}

export interface FoldHinge {
  // horizontal === true means the hinge runs horizontally and the two
  // panes stack vertically; otherwise the panes sit side by side.
  horizontal: boolean;
  // Hinge gap in CSS pixels between the two segments (>= 0).
  gap: number;
}

export interface DeviceLayout {
  deviceClass: UiDeviceClass;
  mode: LayoutMode;
  orientation: Orientation;
  pixelRatio: number;
  // True only for the fold-separated mode: two panes exist and the shell
  // may place two surfaces on them.
  dualPane: boolean;
  hinge: FoldHinge | undefined;
  segments: readonly FoldSegment[];
}

// normalizeSegments validates a raw segment list. Exactly two finite,
// non-degenerate segments that do not overlap represent a separated fold;
// every other shape (0/1/3+ segments, NaN, zero size, overlap) fails soft
// to "no fold information" instead of producing a broken split.
export function normalizeSegments(
  segments: readonly FoldSegment[] | undefined,
): { segments: FoldSegment[]; hinge: FoldHinge } | undefined {
  if (segments === undefined || segments.length !== 2) return undefined;
  const first = segments[0];
  const second = segments[1];
  if (first === undefined || second === undefined) return undefined;
  for (const segment of [first, second]) {
    if (
      !Number.isFinite(segment.x) ||
      !Number.isFinite(segment.y) ||
      !Number.isFinite(segment.width) ||
      !Number.isFinite(segment.height) ||
      segment.width <= 0 ||
      segment.height <= 0
    ) {
      return undefined;
    }
  }
  const overlaps = (axis: "x" | "y", left: FoldSegment, right: FoldSegment): boolean => {
    const leftSize = axis === "x" ? left.width : left.height;
    const rightSize = axis === "x" ? right.width : right.height;
    return left[axis] < right[axis] + rightSize && right[axis] < left[axis] + leftSize;
  };
  // Browsers do not promise a useful segment ordering. Canonicalize the
  // pair by its separation axis before deriving the gap, so the same posture
  // cannot disappear merely because the host returned right-before-left or
  // bottom-before-top.
  if (overlaps("y", first, second) && !overlaps("x", first, second)) {
    const ordered = first.x <= second.x ? [first, second] : [second, first];
    const left = ordered[0];
    const right = ordered[1];
    if (!left || !right) return undefined;
    return {
      segments: ordered,
      hinge: { horizontal: false, gap: right.x - (left.x + left.width) },
    };
  }
  if (overlaps("x", first, second) && !overlaps("y", first, second)) {
    const ordered = first.y <= second.y ? [first, second] : [second, first];
    const top = ordered[0];
    const bottom = ordered[1];
    if (!top || !bottom) return undefined;
    return {
      segments: ordered,
      hinge: { horizontal: true, gap: bottom.y - (top.y + top.height) },
    };
  }
  return undefined;
}

// resolveDeviceLayout is the single pure derivation from viewport facts to
// the shell layout mode. It is total: malformed or hostile input yields the
// most conservative layout, never an exception. Fold-separated requires an
// explicit valid two-segment posture; without one a foldable safely
// degrades to medium (or expanded at desktop widths) — never a forced
// dual pane. Phone keeps compact, tablet medium, desktop expanded.
export function resolveDeviceLayout(input: DeviceLayoutInput): DeviceLayout {
  const width = Number.isFinite(input.viewportWidth) ? input.viewportWidth : 0;
  const height = Number.isFinite(input.viewportHeight) ? input.viewportHeight : 0;
  const pixelRatio =
    input.pixelRatio !== undefined && Number.isFinite(input.pixelRatio) && input.pixelRatio > 0
      ? input.pixelRatio
      : 1;
  const orientation: Orientation = width >= height ? "landscape" : "portrait";
  const fold = normalizeSegments(input.segments);
  const separatedFold = fold !== undefined;
  const deviceClass = classifyDevice(width, separatedFold);
  let mode: LayoutMode;
  switch (deviceClass) {
    case "foldable":
      mode = separatedFold ? "fold-separated" : width <= 1023 ? "medium" : "expanded";
      break;
    case "phone":
      mode = "compact";
      break;
    case "tablet":
      mode = "medium";
      break;
    case "desktop":
      mode = "expanded";
      break;
  }
  return {
    deviceClass,
    mode,
    orientation,
    pixelRatio,
    dualPane: mode === "fold-separated",
    hinge: mode === "fold-separated" && fold ? fold.hinge : undefined,
    segments: fold ? fold.segments : [],
  };
}
