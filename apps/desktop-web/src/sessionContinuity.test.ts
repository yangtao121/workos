// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  clearSessionContinuity,
  readSessionContinuity,
  sessionContinuityKey,
  writeSessionContinuity,
} from "./sessionContinuity.js";

afterEach(() => {
  localStorage.clear();
});
describe("local session journal", () => {
  it("isolates owners, projects and sessions and clears only its own storage", () => {
    const key = sessionContinuityKey("owner", "project", "session");
    expect(writeSessionContinuity(key, { draft: "private draft", pending: [], cursor: 9n })).toBe(
      true,
    );
    expect(readSessionContinuity(key).draft).toBe("private draft");
    for (const other of [
      sessionContinuityKey("other", "project", "session"),
      sessionContinuityKey("owner", "other", "session"),
      sessionContinuityKey("owner", "project", "other"),
    ])
      expect(readSessionContinuity(other).draft).toBe("");
    localStorage.setItem("unrelated", "keep");
    clearSessionContinuity();
    expect(readSessionContinuity(key).cursor).toBe(0n);
    expect(localStorage.getItem("unrelated")).toBe("keep");
  });
  it("fails closed for corrupt receipts without dispatching stored content", () => {
    const key = sessionContinuityKey("owner", "project", "session");
    localStorage.setItem(
      key,
      JSON.stringify({
        draft: "draft",
        cursor: "-1",
        pending: [{ clientInputId: 3, text: "bad" }],
      }),
    );
    expect(readSessionContinuity(key)).toEqual({ draft: "", pending: [], cursor: 0n });
    expect(
      writeSessionContinuity(key, { draft: "x".repeat(20_000), pending: [], cursor: 0n }),
    ).toBe(false);
  });
});
