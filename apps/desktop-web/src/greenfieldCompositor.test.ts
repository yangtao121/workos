import { describe, expect, it } from "vitest";
import { noteKey, releasePressedKeys } from "./greenfieldCompositor.js";

describe("greenfield key release", () => {
  it("forgets pressed keys when focus is lost", () => {
    noteKey("down", "Control");
    noteKey("down", "c");
    noteKey("up", "c");
    expect(releasePressedKeys()).toEqual(["Control"]);
    expect(releasePressedKeys()).toEqual([]);
  });
});
