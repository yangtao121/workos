import { describe, expect, it } from "vitest";
import { classifyDevice } from "./index.js";

describe("classifyDevice", () => {
  it("uses posture before width and otherwise stable breakpoints", () => {
    expect(classifyDevice(500)).toBe("phone");
    expect(classifyDevice(800)).toBe("tablet");
    expect(classifyDevice(1400, true)).toBe("foldable");
  });
});
