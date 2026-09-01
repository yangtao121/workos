import { describe, expect, it } from "vitest";
import { createLayoutStore, createMemoryLayoutStore } from "./store.js";
import { pushRecentId } from "./storage.js";

const PROJECT = "01990000-0000-7000-8000-000000000001";
const OTHER = "01990000-0000-7000-8000-000000000002";
const APP_A = "01990000-0000-7000-8000-0000000000a1";
const NOW = "2026-08-31T10:00:00.000Z";

describe("memory layout store", () => {
  it("starts from an empty record and isolates keys", async () => {
    const store = createMemoryLayoutStore();
    const loaded = await store.load("phone", PROJECT, NOW);
    expect(loaded.revision).toBe(0);
    expect(loaded.projectId).toBe(PROJECT);
    const other = await store.load("desktop", PROJECT, NOW);
    expect(other.deviceClass).toBe("desktop");
    const otherProject = await store.load("phone", OTHER, NOW);
    expect(otherProject.projectId).toBe(OTHER);
  });

  it("serializes concurrent writers so neither write is silently lost", async () => {
    const store = createMemoryLayoutStore();
    // Both tabs read the same base record, then write. The second update
    // must rebase onto the first commit, not clobber it.
    const [first, second] = await Promise.all([
      store.update("phone", PROJECT, NOW, (state) => ({
        ...state,
        activeSystemWindow: "system-monitor",
      })),
      store.update("phone", PROJECT, NOW, (state) => ({
        ...state,
        recentAppInstanceIds: pushRecentId(state.recentAppInstanceIds, APP_A, 8),
      })),
    ]);
    expect(second.revision).toBe(first.revision + 1);
    const final = await store.load("phone", PROJECT, NOW);
    // The last commit's mutation is present; the earlier one was rebased
    // onto a state that no longer carried it — observable via revision.
    expect(final.revision).toBe(2);
    expect(final.activeSystemWindow).toBe("system-monitor");
    expect(final.recentAppInstanceIds).toContain(APP_A);
  });

  it("does not expose mutable references that bypass revision adjudication", async () => {
    const store = createMemoryLayoutStore();
    const written = await store.update("phone", PROJECT, NOW, (state) => ({
      ...state,
      recentAppInstanceIds: [APP_A],
    }));
    written.recentAppInstanceIds.length = 0;
    const loaded = await store.load("phone", PROJECT, NOW);
    expect(loaded.revision).toBe(1);
    expect(loaded.recentAppInstanceIds).toEqual([APP_A]);
    loaded.recentAppInstanceIds.length = 0;
    expect((await store.load("phone", PROJECT, NOW)).recentAppInstanceIds).toEqual([APP_A]);
  });

  it("removeProject and clearAll sweep exactly their scope", async () => {
    const store = createMemoryLayoutStore();
    await store.update("phone", PROJECT, NOW, (state) => state);
    await store.update("desktop", OTHER, NOW, (state) => state);
    await store.removeProject(PROJECT);
    expect((await store.load("phone", PROJECT, NOW)).revision).toBe(0);
    expect((await store.load("desktop", OTHER, NOW)).revision).toBe(1);
    await store.clearAll();
    expect((await store.load("desktop", OTHER, NOW)).revision).toBe(0);
  });
});

describe("createLayoutStore", () => {
  it("falls back to the memory store without IndexedDB", async () => {
    const store = createLayoutStore(undefined);
    const next = await store.update("tablet", PROJECT, NOW, (state) => ({
      ...state,
      layoutPreference: "dual",
    }));
    expect(next.layoutPreference).toBe("dual");
    expect(next.revision).toBe(1);
  });

  it("keeps session state when IndexedDB opening fails", async () => {
    const failingFactory = {
      open: () => {
        throw new Error("storage disabled");
      },
    } as unknown as IDBFactory;
    const store = createLayoutStore(failingFactory);
    await store.update("tablet", PROJECT, NOW, (state) => ({
      ...state,
      activeSystemWindow: "system-monitor",
    }));
    const second = await store.update("tablet", PROJECT, NOW, (state) => ({
      ...state,
      recentAppInstanceIds: [APP_A],
    }));
    expect(second.revision).toBe(2);
    expect(second.activeSystemWindow).toBe("system-monitor");
    expect((await store.load("tablet", PROJECT, NOW)).recentAppInstanceIds).toEqual([APP_A]);
    await store.clearAll();
    expect((await store.load("tablet", PROJECT, NOW)).revision).toBe(0);
  });
});
