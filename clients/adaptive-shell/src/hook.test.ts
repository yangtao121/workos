import { describe, expect, it } from "vitest";
import { resolveDeviceLayout } from "./layout.js";
import { layoutsEqual } from "./hook.js";

describe("layoutsEqual", () => {
  it("detects segment geometry changes even when the hinge gap is unchanged", () => {
    const first = resolveDeviceLayout({
      viewportWidth: 1280,
      viewportHeight: 800,
      segments: [
        { x: 0, y: 0, width: 632, height: 800 },
        { x: 648, y: 0, width: 632, height: 800 },
      ],
    });
    const resized = resolveDeviceLayout({
      viewportWidth: 1280,
      viewportHeight: 800,
      segments: [
        { x: 0, y: 0, width: 600, height: 800 },
        { x: 616, y: 0, width: 664, height: 800 },
      ],
    });
    expect(first.hinge?.gap).toBe(resized.hinge?.gap);
    expect(layoutsEqual(first, resized)).toBe(false);
    expect(layoutsEqual(first, first)).toBe(true);
  });
});
