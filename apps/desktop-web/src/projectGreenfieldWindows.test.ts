import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { GreenfieldWindowSnapshotSchema } from "@workos/protocol";
import { initialWindowState, windowReducer, type WorkOSWindow } from "@workos/window-manager";
import { mergeGreenfieldWindows, projectGreenfieldWindows } from "./projectGreenfieldWindows.js";

const area = { x: 0, y: 0, width: 1440, height: 768 };
const parent: WorkOSWindow = {
  id: "native-workload-1",
  sharedWindowId: "core-window-1",
  kind: "native",
  appId: "native",
  title: "Native",
  projectId: "project-1",
  workloadId: "workload-1",
  rect: { x: 180, y: 80, width: 760, height: 560 },
  restoreRect: { x: 180, y: 80, width: 760, height: 560 },
  mode: "normal",
  zIndex: 2,
};
const docs: WorkOSWindow = {
  ...parent,
  id: "docs",
  sharedWindowId: "core-docs",
  kind: "docs",
  title: "Docs",
  workloadId: undefined,
  zIndex: 3,
};

describe("resident native window shell projection", () => {
  it("maps top-level and transient native windows into the Core parent's z group", () => {
    const snapshot = create(GreenfieldWindowSnapshotSchema, {
      sessionId: "workload-1",
      workloadGeneration: 3n,
      revision: 7n,
      windows: [
        {
          id: "code",
          title: "Code",
          appId: "code",
          zOrder: 1,
          active: true,
          revision: 2n,
          contentRect: { x: 10, y: 10, width: 700, height: 500 },
          visualRect: { x: 10, y: 10, width: 700, height: 500 },
        },
        {
          id: "dialog",
          parentWindowId: "code",
          title: "Save changes",
          appId: "code",
          zOrder: 2,
          revision: 1n,
          contentRect: { x: 110, y: 90, width: 350, height: 220 },
          visualRect: { x: 110, y: 90, width: 350, height: 220 },
        },
      ],
    });
    const children = projectGreenfieldWindows(parent, snapshot, area);
    expect(children.map((item) => item.title)).toEqual(["Code", "Save changes"]);
    expect(children[1]?.nativeParentWindowId).toBe("code");
    expect(children[1]?.rect.x).toBeGreaterThan(children[0]?.rect.x ?? 0);
    const runtime = windowReducer(initialWindowState, {
      type: "reconcile",
      windows: children,
      focusedId: children[0]?.id ?? "",
    });
    const merged = mergeGreenfieldWindows({ windows: [parent, docs], nextZIndex: 4 }, runtime);
    expect(merged.windows[0]?.mode).toBe("minimized");
    expect(merged.windows[1]?.zIndex).toBeGreaterThan(merged.windows[0]?.zIndex ?? 0);
    expect(merged.windows[2]?.zIndex).toBeGreaterThan(merged.windows[1]?.zIndex ?? 0);
    expect(docs.zIndex * 129).toBeGreaterThan(merged.windows[2]?.zIndex ?? 0);
  });

  it("removes native children when the Core workload anchor closes", () => {
    const orphan: WorkOSWindow = {
      ...parent,
      id: "native-window-workload-1-3-code",
      kind: "native-window",
      nativeWindowId: "code",
    };
    const runtime = { windows: [orphan], nextZIndex: 2 };
    expect(mergeGreenfieldWindows({ windows: [], nextZIndex: 1 }, runtime).windows).toEqual([]);
  });

  it("keeps a transient above a locally focused parent and preserves its moved, resized bounds", () => {
    const code: WorkOSWindow = {
      ...parent,
      id: "native-window-workload-1-3-code",
      kind: "native-window",
      title: "Code",
      nativeWindowId: "code",
      zIndex: 1,
    };
    const dialog: WorkOSWindow = {
      ...code,
      id: "native-window-workload-1-3-dialog",
      title: "Open File",
      nativeWindowId: "dialog",
      nativeParentWindowId: "code",
      rect: { x: 320, y: 190, width: 460, height: 300 },
      restoreRect: { x: 320, y: 190, width: 460, height: 300 },
      zIndex: 2,
    };
    const initial = { windows: [code, dialog], nextZIndex: 3 };
    const parentFocused = windowReducer(initial, { type: "focus", id: code.id });
    expect(parentFocused.windows.find((item) => item.id === code.id)?.zIndex).toBeGreaterThan(
      parentFocused.windows.find((item) => item.id === dialog.id)?.zIndex ?? 0,
    );
    const merged = mergeGreenfieldWindows(
      { windows: [parent, docs], nextZIndex: 4 },
      parentFocused,
    );
    const codeZ = merged.windows.find((item) => item.id === code.id)?.zIndex ?? 0;
    const dialogZ = merged.windows.find((item) => item.id === dialog.id)?.zIndex ?? 0;
    expect(dialogZ).toBeGreaterThan(codeZ);
    expect(docs.zIndex * 129).toBeGreaterThan(dialogZ);

    const moved = windowReducer(parentFocused, { type: "move", id: dialog.id, x: 255, y: 155 });
    const resized = windowReducer(moved, {
      type: "resize",
      id: dialog.id,
      width: 500,
      height: 340,
    });
    const refreshed = windowReducer(resized, {
      type: "reconcile",
      windows: [code, dialog],
      focusedId: code.id,
    });
    const retained = refreshed.windows.find((item) => item.id === dialog.id);
    expect(retained?.nativeParentWindowId).toBe("code");
    expect(retained?.rect).toEqual({ x: 255, y: 155, width: 500, height: 340 });
    const refreshedMerged = mergeGreenfieldWindows(
      { windows: [parent, docs], nextZIndex: 4 },
      refreshed,
    );
    expect(refreshedMerged.windows.find((item) => item.id === dialog.id)?.zIndex).toBeGreaterThan(
      refreshedMerged.windows.find((item) => item.id === code.id)?.zIndex ?? 0,
    );
  });
});
