import { describe, expect, it } from "vitest";
import { activeWindow, orderedWindows, secondaryWindow } from "./projection.js";
import { initialWindowState, windowReducer, type WindowState } from "@workos/window-manager";

function stateWithWindows(): WindowState {
  let state = initialWindowState;
  state = windowReducer(state, {
    type: "open",
    window: {
      id: "agent-center",
      appId: "agent-center",
      title: "Agent Center",
      kind: "agent-center",
      rect: { x: 0, y: 0, width: 400, height: 300 },
      mode: "normal",
    },
  });
  state = windowReducer(state, {
    type: "open",
    window: {
      id: "system-monitor",
      appId: "system-monitor",
      title: "System Monitor",
      kind: "system-monitor",
      rect: { x: 10, y: 10, width: 400, height: 300 },
      mode: "normal",
    },
  });
  state = windowReducer(state, {
    type: "open",
    window: {
      id: "artifact-center",
      appId: "artifact-center",
      title: "Artifact Center",
      kind: "artifact-center",
      rect: { x: 20, y: 20, width: 400, height: 300 },
      mode: "normal",
    },
  });
  return state;
}

describe("orderedWindows", () => {
  it("lists visible windows focused-first", () => {
    const ordered = orderedWindows(stateWithWindows());
    expect(ordered.map((window) => window.id)).toEqual([
      "artifact-center",
      "system-monitor",
      "agent-center",
    ]);
  });

  it("excludes minimized windows", () => {
    let state = stateWithWindows();
    state = windowReducer(state, { type: "mode", id: "artifact-center", mode: "minimized" });
    expect(orderedWindows(state).map((window) => window.id)).not.toContain("artifact-center");
  });
});

describe("activeWindow", () => {
  it("prefers the requested window when it is visible", () => {
    const state = stateWithWindows();
    expect(activeWindow(state, "agent-center")?.id).toBe("agent-center");
  });

  it("falls back to the focused window when the preference is gone", () => {
    const state = stateWithWindows();
    expect(activeWindow(state, "vanished")?.id).toBe("artifact-center");
    expect(activeWindow(initialWindowState, "agent-center")).toBeUndefined();
  });
});

describe("secondaryWindow", () => {
  it("picks the next visible window after the active one", () => {
    const state = stateWithWindows();
    const active = activeWindow(state);
    expect(secondaryWindow(state, active?.id)?.id).toBe("system-monitor");
  });

  it("is undefined with a single window", () => {
    let state = initialWindowState;
    state = windowReducer(state, {
      type: "open",
      window: {
        id: "agent-center",
        appId: "agent-center",
        title: "Agent Center",
        kind: "agent-center",
        rect: { x: 0, y: 0, width: 400, height: 300 },
        mode: "normal",
      },
    });
    expect(secondaryWindow(state, "agent-center")).toBeUndefined();
  });
});
