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

// A native top level and its transients move as one stacking family. Local
// focus may raise the parent above its child in the window reducer; keep the
// child above its parent without allowing that family to escape the Core
// workload's single z-order slot.
function stackNativeFamilies(group: WorkOSWindow[]): WorkOSWindow[] {
  const byNativeId = new Map(group.map((item) => [item.nativeWindowId, item]));
  const families = new Map<string, WorkOSWindow[]>();
  for (const item of group) {
    let root = item;
    const seen = new Set([item.id]);
    while (root.nativeParentWindowId) {
      const parent = byNativeId.get(root.nativeParentWindowId);
      if (!parent || seen.has(parent.id)) break;
      root = parent;
      seen.add(parent.id);
    }
    const family = families.get(root.id) ?? [];
    family.push(item);
    families.set(root.id, family);
  }
  const orderedFamilies = [...families.values()].sort((left, right) => {
    const priority = (family: WorkOSWindow[]) => Math.max(...family.map((item) => item.zIndex));
    return (
      priority(left) - priority(right) || (left[0]?.id ?? "").localeCompare(right[0]?.id ?? "")
    );
  });
  const ordered: WorkOSWindow[] = [];
  for (const family of orderedFamilies) {
    const members = [...family].sort(
      (left, right) => left.zIndex - right.zIndex || left.id.localeCompare(right.id),
    );
    const inFamily = new Set(members.map((item) => item.id));
    const visited = new Set<string>();
    const append = (item: WorkOSWindow) => {
      if (visited.has(item.id)) return;
      visited.add(item.id);
      const parent = byNativeId.get(item.nativeParentWindowId ?? "");
      if (parent && inFamily.has(parent.id)) append(parent);
      ordered.push(item);
    };
    members.forEach(append);
  }
  return ordered;
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
    stackNativeFamilies(group).forEach((child, index) =>
      windows.push({
        ...child,
        mode: item.mode === "minimized" ? "minimized" : child.mode,
        zIndex: item.zIndex * 129 + index + 1,
      }),
    );
  }
  return { windows, nextZIndex: core.nextZIndex * 129 };
}
