import { describe, expect, it } from "vitest";
import { initialWindowState, windowReducer, type WindowAction } from "./index.js";

describe("windowReducer", () => {
  it("opens, focuses, and restores a window without losing its normal rect", () => {
    const rect = { x: 10, y: 20, width: 640, height: 480 };
    let state = windowReducer(initialWindowState, {
      type: "open",
      window: {
        id: "agent",
        appId: "agent-center",
        title: "Agent",
        kind: "agent-center",
        rect,
        mode: "normal",
      },
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
        kind: "agent-center",
        rect: { x: 0, y: 0, width: 500, height: 400 },
        mode: "normal",
      },
    });
    state = windowReducer(state, { type: "resize", id: "one", width: 1, height: 1 });
    expect(state.windows[0]?.rect).toMatchObject({ width: 320, height: 220 });
  });

  it("carries the app-surface kind and its session ref without leaking it between windows", () => {
    let state = windowReducer(initialWindowState, {
      type: "open",
      window: {
        id: "surface-1",
        appId: "notes",
        title: "Notes",
        kind: "app-surface",
        surface: {
          surfaceSessionId: "0198d7ea-2110-7c42-b659-c5e4d73bc341",
          url: "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/",
          projectId: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
        },
        rect: { x: 0, y: 0, width: 480, height: 360 },
        mode: "normal",
      },
    });
    state = windowReducer(state, {
      type: "open",
      window: {
        id: "agent-center",
        appId: "agent-center",
        title: "Agent Center",
        kind: "agent-center",
        rect: { x: 0, y: 0, width: 620, height: 520 },
        mode: "normal",
      },
    });
    const surface = state.windows.find((item) => item.id === "surface-1");
    const agent = state.windows.find((item) => item.id === "agent-center");
    expect(surface?.kind).toBe("app-surface");
    expect(surface?.surface?.url).toBe("/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/");
    expect(agent?.kind).toBe("agent-center");
    expect(agent?.surface).toBeUndefined();
  });

  it("deduplicates windows by id so repeat opens cannot create duplicates", () => {
    const open: WindowAction = {
      type: "open",
      window: {
        id: "surface-1",
        appId: "notes",
        title: "Notes",
        kind: "app-surface",
        surface: {
          surfaceSessionId: "s",
          url: "/surfaces/s/",
          projectId: "p",
        },
        rect: { x: 0, y: 0, width: 480, height: 360 },
        mode: "normal",
      },
    };
    let state = windowReducer(initialWindowState, open);
    const firstZ = state.windows[0]?.zIndex;
    state = windowReducer(state, open);
    expect(state.windows).toHaveLength(1);
    expect(state.windows[0]?.zIndex).toBeGreaterThan(firstZ ?? 0);
  });
});
