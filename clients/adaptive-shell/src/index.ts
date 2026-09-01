export {
  COMPACT_MAX_WIDTH,
  MEDIUM_MAX_WIDTH,
  classifyDevice,
  deviceClassFromProto,
  isUiDeviceClass,
  protoFromDeviceClass,
  type FoldSegment,
  type LayoutMode,
  type Orientation,
  type UiDeviceClass,
} from "./device.js";
export {
  normalizeSegments,
  resolveDeviceLayout,
  type DeviceLayout,
  type DeviceLayoutInput,
  type FoldHinge,
} from "./layout.js";
export { orderedWindows, activeWindow, secondaryWindow } from "./projection.js";
export {
  DOCK_APP_INSTANCE_LIMIT,
  LAYOUT_SCHEMA_VERSION,
  RECENT_APP_INSTANCE_LIMIT,
  SYSTEM_WINDOW_IDS,
  emptyLayoutState,
  isValidCanonicalUuid,
  layoutKey,
  migrateLayoutState,
  pruneLayoutState,
  pushRecentId,
  sanitizeLayoutState,
  type DeviceLayoutState,
  type LayoutPreference,
} from "./storage.js";
export { createLayoutStore, createMemoryLayoutStore, type LayoutStore } from "./store.js";
export { useDeviceLayout } from "./hook.js";
