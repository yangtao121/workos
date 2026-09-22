// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  clearSessionContinuity,
  coalesceSessionRefresh,
  patchSessionContinuity,
  readSessionContinuity,
  sessionContinuityGeneration,
  sessionContinuityKey,
  writeSessionContinuity,
} from "./sessionContinuity.js";

afterEach(async () => {
  await clearSessionContinuity();
  localStorage.clear();
});
const key = sessionContinuityKey("owner", "project", "session");
const receipt = (clientInputId: string) => ({
  clientInputId,
  text: "Message " + clientInputId,
  phase: "submitting" as const,
});
describe("transactional local session journal", () => {
  it("isolates owner/project/session and clears only its own content", async () => {
    expect(
      await writeSessionContinuity(key, { draft: "private draft", pending: [], cursor: 9n }),
    ).toBe(true);
    expect((await readSessionContinuity(key)).draft).toBe("private draft");
    for (const other of [
      sessionContinuityKey("other", "project", "session"),
      sessionContinuityKey("owner", "other", "session"),
      sessionContinuityKey("owner", "project", "other"),
    ])
      expect((await readSessionContinuity(other)).draft).toBe("");
    localStorage.setItem("unrelated", "keep");
    await clearSessionContinuity();
    expect((await readSessionContinuity(key)).cursor).toBe(0n);
    expect(localStorage.getItem("unrelated")).toBe("keep");
  });
  it("merges concurrent receipts without an idle tab overwriting another draft", async () => {
    await Promise.all([
      patchSessionContinuity(key, { addPending: [receipt("a")] }),
      patchSessionContinuity(key, { addPending: [receipt("b")] }),
      patchSessionContinuity(key, { draft: "New draft" }),
    ]);
    await patchSessionContinuity(key, { removePendingIds: [], cursor: 5n });
    let state = await readSessionContinuity(key);
    expect(state.draft).toBe("New draft");
    expect(state.pending.map((v) => v.clientInputId).sort()).toEqual(["a", "b"]);
    await patchSessionContinuity(key, { removePendingIds: ["a"], cursor: 2n });
    state = await readSessionContinuity(key);
    expect(state.pending.map((v) => v.clientInputId)).toEqual(["b"]);
    expect(state.cursor).toBe(5n);
    expect(state.draft).toBe("New draft");
  });
  it("does not clear a newer tab's draft when another tab submits an old draft", async () => {
    await patchSessionContinuity(key, { draft: "New draft" });
    await patchSessionContinuity(key, { addPending: [receipt("a")], clearDraftIf: "Old draft" });
    expect((await readSessionContinuity(key)).draft).toBe("New draft");
    await patchSessionContinuity(key, { clearDraftIf: "New draft" });
    expect((await readSessionContinuity(key)).draft).toBe("");
  });
  it("fences pending writers across Forget, including another tab's old epoch", async () => {
    const old = await readSessionContinuity(key);
    const guard = { epoch: old.epoch ?? "", generation: sessionContinuityGeneration() };
    const queued = patchSessionContinuity(key, { draft: "queued" }, guard);
    await clearSessionContinuity();
    await queued;
    expect(await patchSessionContinuity(key, { draft: "late" }, guard)).toBe(false);
    expect(
      await patchSessionContinuity(
        key,
        { draft: "other tab late" },
        { ...guard, generation: sessionContinuityGeneration() },
      ),
    ).toBe(false);
    expect((await readSessionContinuity(key)).draft).toBe("");
  });
  it("rejects writes outside cursor and receipt bounds without consuming content", async () => {
    expect(
      await writeSessionContinuity(key, { draft: "x".repeat(20_000), pending: [], cursor: 0n }),
    ).toBe(false);
    expect(await patchSessionContinuity(key, { cursor: 1n << 63n })).toBe(false);
    expect(
      await patchSessionContinuity(key, {
        addPending: Array.from({ length: 17 }, (_, i) => receipt(String(i))),
      }),
    ).toBe(false);
    expect((await readSessionContinuity(key)).pending).toEqual([]);
  });
  it("coalesces slow refresh requests while committing each successful snapshot", async () => {
    const releases: ((value: boolean) => void)[] = [];
    const painted: number[] = [];
    const load = vi.fn(async () => {
      const index = releases.length;
      const result = await new Promise<boolean>((resolve) => releases.push(resolve));
      painted.push(index);
      return result;
    });
    const refresh = coalesceSessionRefresh(load);
    const first = refresh();
    const second = refresh();
    const third = refresh();
    expect(load).toHaveBeenCalledTimes(1);
    releases[0]?.(true);
    expect(await first).toBe(true);
    expect(painted).toEqual([0]);
    expect(load).toHaveBeenCalledTimes(2);
    releases[1]?.(true);
    expect(await second).toBe(true);
    expect(await third).toBe(true);
    expect(painted).toEqual([0, 1]);
    expect(load).toHaveBeenCalledTimes(2);
  });
});
