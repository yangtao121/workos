import { describe, expect, it } from "vitest";
import { initialWindowState, windowReducer } from "./index.js";

describe("windowReducer", () => {
  it("opens, focuses, and restores a window without losing its normal rect", () => {
    const rect = { x: 10, y: 20, width: 640, height: 480 };
    let state = windowReducer(initialWindowState, {
      type: "open",
      window: { id: "agent", appId: "agent-center", title: "Agent", rect, mode: "normal" },
    });
    state = windowReducer(state, { type: "mode", id: "agent", mode: "maximized" });
    state = windowReducer(state, { type: "mode", id: "agent", mode: "normal" });
    expect(state.windows[0]?.rect).toEqual(rect);
    expect(state.windows[0]?.zIndex).toBeGreaterThan(1);
  });

  it("enforces the minimum normal window size", () => {
    let state = windowReducer(initialWindowState, {
      type: "open",
      window: {
        id: "one",
        appId: "test",
        title: "Test",
        rect: { x: 0, y: 0, width: 500, height: 400 },
        mode: "normal",
      },
    });
    state = windowReducer(state, { type: "resize", id: "one", width: 1, height: 1 });
    expect(state.windows[0]?.rect).toMatchObject({ width: 320, height: 220 });
  });
});
