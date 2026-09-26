import { fitRect, type Rect, type WindowState, type WorkOSWindow } from "@workos/window-manager";
import type { GreenfieldWindowSnapshot } from "@workos/protocol";

// Core keeps the one durable workload reference. These window entities are
// transient Runtime facts and are never sent as DesktopWindowTargets to Core.
export function projectGreenfieldWindows(
  parent: WorkOSWindow,
  snapshot: GreenfieldWindowSnapshot,
  workArea: Rect,
): WorkOSWindow[] {
  const visual = snapshot.windows
    .map((item) => item.visualRect)
    .filter((rect) => rect !== undefined);
  const originX = Math.min(...visual.map((rect) => rect.x));
  const originY = Math.min(...visual.map((rect) => rect.y));
  return [...snapshot.windows]
    .sort((left, right) => left.zOrder - right.zOrder || left.id.localeCompare(right.id))
    .flatMap((item) => {
      const rect = item.visualRect;
      if (!rect) return [];
      const bounds = fitRect(
        {
          x: parent.rect.x + rect.x - originX,
          y: parent.rect.y + rect.y - originY,
          width: Math.max(320, rect.width),
          height: Math.max(220, rect.height + 38),
        },
        workArea,
      );
      return [
        {
          id: `native-window-${snapshot.sessionId}-${String(snapshot.workloadGeneration)}-${item.id}`,
          sharedWindowId: parent.sharedWindowId,
          kind: "native-window" as const,
          appId: item.appId || parent.appId,
          title: item.title || "Native window",
          projectId: parent.projectId,
          workloadId: parent.workloadId,
          expectedWorkloadId: parent.expectedWorkloadId,
          expectedWorkloadGeneration: snapshot.workloadGeneration,
          sessionId: snapshot.sessionId,
          nativeWindowId: item.id,
          nativeParentWindowId: item.parentWindowId,
          rect: bounds,
          restoreRect: bounds,
          mode: "normal" as const,
          zIndex: 0,
        },
      ];
    });
}

// A native workload occupies one Core z-order slot. Its top-levels are
// ordered within that slot, so focusing another WorkOS app raises it above
// every native child and focusing a native child raises the whole group.
export function mergeGreenfieldWindows(core: WindowState, runtime: WindowState): WindowState {
  const byWorkload = new Map<string, WorkOSWindow[]>();
  for (const item of runtime.windows) {
    const key = item.workloadId ?? "";
    const group = byWorkload.get(key) ?? [];
    group.push(item);
    byWorkload.set(key, group);
  }
  const windows: WorkOSWindow[] = [];
  for (const item of core.windows) {
    const group = byWorkload.get(item.workloadId ?? "") ?? [];
    windows.push({
      ...item,
      zIndex: item.zIndex * 129,
      // Keep the lifecycle host mounted in the expanded shell without
      // showing a second empty manager window beside the native top-levels.
      mode: item.kind === "native" && group.length > 0 ? "minimized" : item.mode,
    });
    if (item.kind !== "native") continue;
    group.sort((left, right) => left.zIndex - right.zIndex);
    group.forEach((child, index) =>
      windows.push({
        ...child,
        mode: item.mode === "minimized" ? "minimized" : child.mode,
        zIndex: item.zIndex * 129 + index + 1,
      }),
    );
  }
  return { windows, nextZIndex: core.nextZIndex * 129 };
}
