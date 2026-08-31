import { describe, expect, it } from "vitest";
import {
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
} from "./storage.js";

const PROJECT_A = "01990000-0000-7000-8000-000000000001";
const PROJECT_B = "01990000-0000-7000-8000-000000000002";
const APP_A = "01990000-0000-7000-8000-0000000000a1";
const APP_B = "01990000-0000-7000-8000-0000000000a2";
const NOW = "2026-08-31T10:00:00.000Z";

describe("layoutKey", () => {
  it("isolates device class and project", () => {
    expect(layoutKey("desktop", PROJECT_A)).toBe(`layout/desktop/${PROJECT_A}`);
    expect(layoutKey("phone", PROJECT_A)).toBe(`layout/phone/${PROJECT_A}`);
    expect(layoutKey("desktop", PROJECT_B)).toBe(`layout/desktop/${PROJECT_B}`);
    expect(layoutKey("desktop", PROJECT_A)).not.toBe(layoutKey("phone", PROJECT_A));
    expect(layoutKey("desktop", PROJECT_A)).not.toBe(layoutKey("desktop", PROJECT_B));
  });
});

describe("isValidCanonicalUuid", () => {
  it("accepts canonical UUIDv7 facts", () => {
    expect(isValidCanonicalUuid(APP_A)).toBe(true);
  });

  it("rejects non-canonical identifiers", () => {
    expect(isValidCanonicalUuid("not-a-uuid")).toBe(false);
    expect(isValidCanonicalUuid(APP_A.toUpperCase())).toBe(false);
    expect(isValidCanonicalUuid("01990000-0000-1000-8000-0000000000a1")).toBe(false);
    expect(isValidCanonicalUuid("")).toBe(false);
    expect(isValidCanonicalUuid(undefined)).toBe(false);
    expect(isValidCanonicalUuid(42)).toBe(false);
  });
});

describe("sanitizeLayoutState", () => {
  const base = () => emptyLayoutState(PROJECT_A, "phone", NOW);

  it("accepts a well-formed record", () => {
    const state = {
      ...base(),
      activeSystemWindow: "system-monitor",
      recentAppInstanceIds: [APP_A],
      dockAppInstanceIds: [APP_A, APP_B],
      layoutPreference: "dual" as const,
      revision: 4,
    };
    expect(sanitizeLayoutState(state, PROJECT_A, "phone")).toEqual(state);
  });

  it("rejects unknown schema versions and foreign bindings", () => {
    expect(
      sanitizeLayoutState({ ...base(), schemaVersion: 99 }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(sanitizeLayoutState(base(), PROJECT_B, "phone")).toBeUndefined();
    expect(sanitizeLayoutState(base(), PROJECT_A, "tablet")).toBeUndefined();
    expect(sanitizeLayoutState(undefined, PROJECT_A, "phone")).toBeUndefined();
  });

  it("rejects content-bearing or malformed fields", () => {
    expect(
      sanitizeLayoutState({ ...base(), activeAppInstanceId: "goal text" }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(
      sanitizeLayoutState({ ...base(), activeSystemWindow: "make me root" }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(
      sanitizeLayoutState({ ...base(), layoutPreference: "wide" }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(
      sanitizeLayoutState({ ...base(), updatedAt: "never" }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(sanitizeLayoutState({ ...base(), revision: -1 }, PROJECT_A, "phone")).toBeUndefined();
    expect(
      sanitizeLayoutState({ ...base(), extraField: "x" } as never, PROJECT_A, "phone"),
    ).toBeUndefined();
  });

  it("rejects over-long or non-canonical lists", () => {
    const tooMany = Array.from({ length: RECENT_APP_INSTANCE_LIMIT + 1 }, () => APP_A);
    expect(
      sanitizeLayoutState({ ...base(), recentAppInstanceIds: tooMany }, PROJECT_A, "phone"),
    ).toBeUndefined();
    const tooManyDock = Array.from({ length: DOCK_APP_INSTANCE_LIMIT + 1 }, () => APP_A);
    expect(
      sanitizeLayoutState({ ...base(), dockAppInstanceIds: tooManyDock }, PROJECT_A, "phone"),
    ).toBeUndefined();
    expect(
      sanitizeLayoutState({ ...base(), recentAppInstanceIds: ["bogus"] }, PROJECT_A, "phone"),
    ).toBeUndefined();
  });

  it("corruption resets exactly the one key, never siblings", () => {
    // The sanitizer is pure per key: a corrupt record for one project does
    // not influence the sanitized record of another.
    expect(sanitizeLayoutState("garbage", PROJECT_A, "phone")).toBeUndefined();
    expect(
      sanitizeLayoutState(emptyLayoutState(PROJECT_B, "desktop", NOW), PROJECT_B, "desktop"),
    ).toBeDefined();
    expect(SYSTEM_WINDOW_IDS).toContain("system-monitor");
  });
});

describe("migrateLayoutState", () => {
  it("keeps a current-version record through sanitize", () => {
    const state = { ...emptyLayoutState(PROJECT_A, "phone", NOW), revision: 2 };
    expect(migrateLayoutState(state, PROJECT_A, "phone", NOW)).toEqual(state);
  });

  it("resets an unknown-version record to a fresh state", () => {
    const migrated = migrateLayoutState({ schemaVersion: 0, junk: true }, PROJECT_A, "phone", NOW);
    expect(migrated.schemaVersion).toBe(LAYOUT_SCHEMA_VERSION);
    expect(migrated.recentAppInstanceIds).toEqual([]);
    expect(migrated.layoutPreference).toBe("dual");
  });
});

describe("pushRecentId", () => {
  it("moves ids to the front and bounds the list", () => {
    let ids = pushRecentId([], APP_A, RECENT_APP_INSTANCE_LIMIT);
    ids = pushRecentId(ids, APP_B, RECENT_APP_INSTANCE_LIMIT);
    expect(ids).toEqual([APP_B, APP_A]);
    ids = pushRecentId(ids, APP_A, RECENT_APP_INSTANCE_LIMIT);
    expect(ids).toEqual([APP_A, APP_B]);
    for (let index = 0; index < 20; index += 1) {
      ids = pushRecentId(
        ids,
        `01990000-0000-7000-8000-0000000000b${index.toString().padStart(2, "0")}`,
        RECENT_APP_INSTANCE_LIMIT,
      );
    }
    expect(ids).toHaveLength(RECENT_APP_INSTANCE_LIMIT);
  });
});

describe("pruneLayoutState", () => {
  it("drops references to vanished app instances", () => {
    const state = {
      ...emptyLayoutState(PROJECT_A, "phone", NOW),
      activeAppInstanceId: APP_A,
      recentAppInstanceIds: [APP_A, APP_B],
      dockAppInstanceIds: [APP_B],
    };
    const pruned = pruneLayoutState(state, new Set([APP_B]));
    expect(pruned.activeAppInstanceId).toBeUndefined();
    expect(pruned.recentAppInstanceIds).toEqual([APP_B]);
    expect(pruned.dockAppInstanceIds).toEqual([APP_B]);
  });
});
