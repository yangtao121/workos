import { DeviceClass } from "@workos/protocol";

// The single shared device/layout contract (docs/structure.md 12.2). The
// canonical device vocabulary is the proto `workos.surface.v1.DeviceClass`;
// this module is the only place that maps it to the shell's layout classes,
// so no client maintains a second enum with drifting meaning.

export type UiDeviceClass = "phone" | "tablet" | "foldable" | "desktop";

export type LayoutMode = "compact" | "medium" | "expanded" | "fold-separated";

// isUiDeviceClass guards an untrusted string against the class vocabulary.
export function isUiDeviceClass(value: unknown): value is UiDeviceClass {
  return value === "phone" || value === "tablet" || value === "foldable" || value === "desktop";
}

export type Orientation = "portrait" | "landscape";

// One fold segment in CSS pixels (the Window Segments Management API shape,
// narrowed to the fields the layout derivation needs).
export interface FoldSegment {
  x: number;
  y: number;
  width: number;
  height: number;
}

// deviceClassFromProto maps the canonical proto enum to the shell classes.
// Unspecified or unknown numeric values have no shell meaning: the mapping
// fails soft to `undefined` and callers keep their previous class instead of
// guessing.
export function deviceClassFromProto(value: DeviceClass): UiDeviceClass | undefined {
  switch (value) {
    case DeviceClass.DESKTOP:
      return "desktop";
    case DeviceClass.TABLET:
      return "tablet";
    case DeviceClass.FOLDABLE:
      return "foldable";
    case DeviceClass.PHONE:
      return "phone";
    default:
      return undefined;
  }
}

// protoFromDeviceClass is the reverse mapping used when a client must send
// its device class to CreateSurface.
export function protoFromDeviceClass(value: UiDeviceClass): DeviceClass {
  switch (value) {
    case "desktop":
      return DeviceClass.DESKTOP;
    case "tablet":
      return DeviceClass.TABLET;
    case "foldable":
      return DeviceClass.FOLDABLE;
    case "phone":
      return DeviceClass.PHONE;
  }
}

// Viewport breakpoints shared by every client: <600 is a phone, <1024 a
// tablet, everything else a desktop. The boundaries are inclusive-exclusive
// so 599/600 and 1023/1024 are stable, tested facts.
export const COMPACT_MAX_WIDTH = 599;
export const MEDIUM_MAX_WIDTH = 1023;

// classifyDevice derives the device class from the viewport width; a real
// separated fold posture (two window segments) always wins over width, per
// the same rule the shell has always used. There is deliberately no
// user-agent sniffing or vendor list here.
export function classifyDevice(width: number, separatedFold = false): UiDeviceClass {
  if (separatedFold) return "foldable";
  if (!(width >= COMPACT_MAX_WIDTH + 1)) return "phone";
  if (width <= MEDIUM_MAX_WIDTH) return "tablet";
  return "desktop";
}
