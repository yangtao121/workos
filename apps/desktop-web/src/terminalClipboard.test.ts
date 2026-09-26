import { describe, expect, it } from "vitest";
import {
  MAX_TERMINAL_CLIPBOARD_BYTES,
  MAX_TERMINAL_WRITE_BYTES,
  terminalClipboardChunks,
} from "./terminalClipboard.js";

describe("terminalClipboardChunks", () => {
  it("preserves long UTF-8 text while every ordered PTY write remains bounded", () => {
    const original = "中文🙂\tline one\nline two\n".repeat(1500);
    const chunks = terminalClipboardChunks(original);
    expect(original.length).toBeGreaterThan(256);
    expect(chunks.length).toBeGreaterThan(1);
    const decoder = new TextDecoder("utf-8", { fatal: true });
    expect(
      chunks.every((chunk) => chunk.length > 0 && chunk.length <= MAX_TERMINAL_WRITE_BYTES),
    ).toBe(true);
    expect(chunks.map((chunk) => decoder.decode(chunk)).join("")).toBe(original);
  });

  it("rejects empty, NUL and over-budget clipboard text before any write", () => {
    expect(() => terminalClipboardChunks("")).toThrow(RangeError);
    expect(() => terminalClipboardChunks("hello\0world")).toThrow(RangeError);
    expect(() => terminalClipboardChunks("x".repeat(MAX_TERMINAL_CLIPBOARD_BYTES + 1))).toThrow(
      RangeError,
    );
  });
});
