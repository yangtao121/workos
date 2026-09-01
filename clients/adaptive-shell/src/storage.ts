import type { UiDeviceClass } from "./device.js";

// Device-local UI layout state (docs/structure.md 12.6). Everything in here
// is a bounded UI reference: canonical IDs, placement preferences, and a
// layout preference. Bridge tokens, cookies, credentials, task goals,
// artifact content, provider output, or any user content never enter this
// record — the type has no field for them, and the sanitizer rejects
// unknown fields so older builds cannot smuggle them in either.

export const LAYOUT_SCHEMA_VERSION = 1;

export type LayoutPreference = "single" | "dual";

export interface DeviceLayoutState {
  schemaVersion: typeof LAYOUT_SCHEMA_VERSION;
  projectId: string;
  deviceClass: UiDeviceClass;
  activeAppInstanceId: string | undefined;
  // The active system window id ("agent-center", "system-monitor",
  // "device-center", "artifact-center"); never free-form content.
  activeSystemWindow: string | undefined;
  activeArtifactId: string | undefined;
  recentAppInstanceIds: string[];
  dockAppInstanceIds: string[];
  layoutPreference: LayoutPreference;
  // UTC instant of the last adjudicated write (ISO-8601).
  updatedAt: string;
  // Monotonic per-key write counter. Multi-tab writes serialize inside one
  // IndexedDB transaction and the mutator always re-applies onto the
  // freshest stored record, so a stale tab can never silently clobber a
  // newer write; the revision makes every such rebase observable.
  revision: number;
}

export const RECENT_APP_INSTANCE_LIMIT = 8;
export const DOCK_APP_INSTANCE_LIMIT = 12;

// The bounded set of system window ids the shell recognizes. Anything else
// in a stored record is corruption and resets the key.
export const SYSTEM_WINDOW_IDS: readonly string[] = [
  "agent-center",
  "system-monitor",
  "device-center",
  "artifact-center",
];

// Canonical UUIDv7: lowercase hyphenated 8-4-4-4-12 hex, version nibble 7,
// RFC variant. All server-minted references stored here are UUIDv7.
const UUID_V7_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

export function isValidCanonicalUuid(value: unknown): value is string {
  return typeof value === "string" && UUID_V7_PATTERN.test(value);
}

// layoutKey isolates one record per device class and project inside this
// browser profile's storage. Desktop geometry can therefore never overwrite
// phone or tablet state, and two projects never share a record.
export function layoutKey(deviceClass: UiDeviceClass, projectId: string): string {
  return `layout/${deviceClass}/${projectId}`;
}

export function emptyLayoutState(
  projectId: string,
  deviceClass: UiDeviceClass,
  now: string,
): DeviceLayoutState {
  return {
    schemaVersion: LAYOUT_SCHEMA_VERSION,
    projectId,
    deviceClass,
    activeAppInstanceId: undefined,
    activeSystemWindow: undefined,
    activeArtifactId: undefined,
    recentAppInstanceIds: [],
    dockAppInstanceIds: [],
    // A foldable that reports two segments shows both panes by default;
    // the user's explicit single-pane choice is the persisted override.
    layoutPreference: "dual",
    updatedAt: now,
    revision: 0,
  };
}

function validTimestamp(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const parsed = new Date(value);
  try {
    // Layout writes originate from Date#toISOString. Requiring the exact
    // canonical millisecond UTC spelling prevents offsets and permissive
    // Date.parse inputs from becoming durable facts.
    return parsed.toISOString() === value;
  } catch {
    return false;
  }
}

function validIdList(value: unknown, limit: number): value is string[] {
  if (!Array.isArray(value) || value.length > limit) return false;
  const seen = new Set<string>();
  for (const entry of value) {
    if (!isValidCanonicalUuid(entry) || seen.has(entry)) return false;
    seen.add(entry);
  }
  return true;
}

function dedupeBounded(ids: string[], limit: number): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const id of ids) {
    if (seen.has(id)) continue;
    seen.add(id);
    result.push(id);
    if (result.length >= limit) break;
  }
  return result;
}

// sanitizeLayoutState re-derives a trustworthy record from untrusted stored
// bytes. Any shape violation — unknown schema version, foreign project or
// device class, non-canonical IDs, over-long lists, bad timestamps, unknown
// fields — marks the whole record corrupt (`undefined`), and the caller
// resets exactly this key. Corruption never resets sibling keys.
export function sanitizeLayoutState(
  raw: unknown,
  projectId: string,
  deviceClass: UiDeviceClass,
): DeviceLayoutState | undefined {
  if (!isValidCanonicalUuid(projectId)) return undefined;
  if (typeof raw !== "object" || raw === null) return undefined;
  const record = raw as Record<string, unknown>;
  const knownFields: ReadonlySet<string> = new Set([
    "schemaVersion",
    "projectId",
    "deviceClass",
    "activeAppInstanceId",
    "activeSystemWindow",
    "activeArtifactId",
    "recentAppInstanceIds",
    "dockAppInstanceIds",
    "layoutPreference",
    "updatedAt",
    "revision",
  ]);
  // Unknown fields mean the bytes were not written by this schema version:
  // fail closed for exactly this key instead of silently carrying foreign
  // payloads forward.
  for (const field of Object.keys(record)) {
    if (!knownFields.has(field)) return undefined;
  }
  if (record.schemaVersion !== LAYOUT_SCHEMA_VERSION) return undefined;
  if (record.projectId !== projectId || record.deviceClass !== deviceClass) return undefined;
  if (
    !validTimestamp(record.updatedAt) ||
    typeof record.revision !== "number" ||
    !Number.isSafeInteger(record.revision) ||
    record.revision < 0
  ) {
    return undefined;
  }
  if (record.layoutPreference !== "single" && record.layoutPreference !== "dual") {
    return undefined;
  }
  const activeAppInstanceId = record.activeAppInstanceId;
  if (activeAppInstanceId !== undefined && !isValidCanonicalUuid(activeAppInstanceId)) {
    return undefined;
  }
  const activeArtifactId = record.activeArtifactId;
  if (activeArtifactId !== undefined && !isValidCanonicalUuid(activeArtifactId)) {
    return undefined;
  }
  const activeSystemWindow = record.activeSystemWindow;
  if (
    activeSystemWindow !== undefined &&
    !(typeof activeSystemWindow === "string" && SYSTEM_WINDOW_IDS.includes(activeSystemWindow))
  ) {
    return undefined;
  }
  const recentAppInstanceIds = record.recentAppInstanceIds;
  if (!validIdList(recentAppInstanceIds, RECENT_APP_INSTANCE_LIMIT)) return undefined;
  const dockAppInstanceIds = record.dockAppInstanceIds;
  if (!validIdList(dockAppInstanceIds, DOCK_APP_INSTANCE_LIMIT)) return undefined;
  return {
    schemaVersion: LAYOUT_SCHEMA_VERSION,
    projectId,
    deviceClass,
    activeAppInstanceId,
    activeSystemWindow,
    activeArtifactId,
    recentAppInstanceIds,
    dockAppInstanceIds,
    layoutPreference: record.layoutPreference,
    updatedAt: record.updatedAt,
    revision: record.revision,
  };
}

// migrateLayoutState upgrades an older stored record to the current schema.
// Version 1 is the first version, so the only honest migrations are
// "already current" and "unknown version → fresh record" (fail closed for
// exactly this key; never a whole-store wipe). Future versions extend the
// switch with explicit, tested steps.
export function migrateLayoutState(
  raw: unknown,
  projectId: string,
  deviceClass: UiDeviceClass,
  now: string,
): DeviceLayoutState {
  if (
    typeof raw === "object" &&
    raw !== null &&
    (raw as Record<string, unknown>).schemaVersion === LAYOUT_SCHEMA_VERSION
  ) {
    const sanitized = sanitizeLayoutState(raw, projectId, deviceClass);
    if (sanitized) return sanitized;
  }
  return emptyLayoutState(projectId, deviceClass, now);
}

// pushRecentId moves one app instance to the front of a bounded recency
// list, deduplicating.
export function pushRecentId(ids: string[], id: string, limit: number): string[] {
  return dedupeBounded([id, ...ids], limit);
}

// pruneLayoutState drops references to app instances that no longer exist
// (uninstall, grant/revision churn, archive) and clears active fields that
// point at them. A late write from a stale tab can therefore not resurrect
// a removed instance: every persisted mutation re-prunes with the caller's
// current valid set.
export function pruneLayoutState(
  state: DeviceLayoutState,
  validAppInstanceIds: ReadonlySet<string>,
): DeviceLayoutState {
  const keep = (ids: string[]) => ids.filter((id) => validAppInstanceIds.has(id));
  const activeAppInstanceId =
    state.activeAppInstanceId !== undefined && validAppInstanceIds.has(state.activeAppInstanceId)
      ? state.activeAppInstanceId
      : undefined;
  return { ...state, activeAppInstanceId, ...prunedLists(state, keep) };
}

function prunedLists(
  state: DeviceLayoutState,
  keep: (ids: string[]) => string[],
): Pick<DeviceLayoutState, "recentAppInstanceIds" | "dockAppInstanceIds"> {
  return {
    recentAppInstanceIds: keep(state.recentAppInstanceIds),
    dockAppInstanceIds: keep(state.dockAppInstanceIds),
  };
}
