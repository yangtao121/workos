import { describe, expect, it } from "vitest";
import { normalApplicationExit, parseProcessIdentity, processAlive } from "./applicationExit.js";

describe("exact native application lifecycle", () => {
  it("reads starttime after a command containing spaces and parentheses", () => {
    const stat = `42 (code (editor) main) S ${Array.from({ length: 18 }, (_, i) => i + 1).join(" ")} 812345 0 0`;
    expect(parseProcessIdentity(stat)).toEqual({ state: "S", starttime: "812345" });
    expect(processAlive("812345", parseProcessIdentity(stat))).toBe(true);
    expect(processAlive("812346", parseProcessIdentity(stat))).toBe(false);
    expect(processAlive("812345", { state: "Z", starttime: "812345" })).toBe(false);
    expect(processAlive("812345", undefined)).toBe(false);
  });

  it("only treats an exact clean process exit as stopped", () => {
    expect(normalApplicationExit({ exitCode: 0 })).toBe(true);
    expect(normalApplicationExit({ exitCode: 1 })).toBe(false);
    expect(normalApplicationExit({ signal: "SIGKILL" })).toBe(false);
    expect(normalApplicationExit(undefined)).toBe(false);
  });
});
