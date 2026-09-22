import { create } from "@bufbuild/protobuf";
import {
  DesktopWindowTargetSchema,
  SurfaceRenderer,
  type DesktopWindow,
  type DesktopWindowTarget,
  type SurfaceSession,
} from "@workos/protocol";
import { fitRect, type WorkOSWindow } from "@workos/window-manager";

export const GLOBAL_WINDOWS = new Set([
  "home",
  "device-center",
  "notification-center",
  "system-monitor",
  "mission-control",
  "browser",
]);
const TITLES: Record<string, string> = {
  home: "Home",
  "agent-center": "Agent Center",
  "agent-sessions": "Agent Sessions",
  "workspace-previews": "Development previews",
  "app-surface": "App",
  "system-monitor": "System Monitor",
  "device-center": "Device Center",
  "artifact-center": "Artifact Center",
  "artifact-viewer": "Artifact Review",
  "knowledge-center": "Knowledge Center",
  "notification-center": "Notifications",
  "mission-control": "Mission Control",
  "app-library": "App Library",
  settings: "Project settings",
  files: "Files",
  docs: "Docs",
  code: "Code",
  browser: "Browser",
  terminal: "Terminal",
  native: "Native",
};
export function targetForWindow(
  item: Pick<
    WorkOSWindow,
    | "kind"
    | "appId"
    | "artifact"
    | "surface"
    | "projectId"
    | "workloadId"
    | "expectedWorkloadId"
    | "expectedWorkloadGeneration"
    | "previewId"
    | "sessionId"
  >,
  projectId: string,
): DesktopWindowTarget {
  const target = create(DesktopWindowTargetSchema, {
    kind: item.kind,
    projectId: GLOBAL_WINDOWS.has(item.kind)
      ? ""
      : (item.projectId ?? item.artifact?.projectId ?? item.surface?.projectId ?? projectId),
  });
  if (item.kind === "app-surface") target.resource = { case: "appInstanceId", value: item.appId };
  else if (item.artifact) target.resource = { case: "artifactId", value: item.artifact.artifactId };
  else if (item.workloadId) target.resource = { case: "workloadId", value: item.workloadId };
  else if (item.previewId) target.resource = { case: "previewId", value: item.previewId };
  else if (item.sessionId) target.resource = { case: "sessionId", value: item.sessionId };
  if (item.kind === "app-surface" || item.kind === "terminal" || item.kind === "native") {
    target.expectedWorkloadId = item.expectedWorkloadId ?? item.workloadId ?? "";
    target.expectedWorkloadGeneration = item.expectedWorkloadGeneration ?? 0n;
  }
  return target;
}
export function appSurfaceKey(target: DesktopWindowTarget) {
  return `${target.projectId}:${target.resource.value ?? ""}:${target.expectedWorkloadId}:${String(target.expectedWorkloadGeneration)}`;
}
export function projectSharedWindow(
  item: DesktopWindow,
  surface?: SurfaceSession,
): WorkOSWindow | undefined {
  const target = item.target;
  if (!target || !TITLES[target.kind]) return undefined;
  const resource = target.resource;
  // Stable shell aliases keep automation and local geometry independent of
  // the server-minted logical ID. Resource windows remain independently keyed.
  const id = ["app-surface", "artifact-viewer", "terminal", "native"].includes(target.kind)
    ? `${target.kind}-${resource.value ?? item.id}`
    : target.kind;
  const rect = fitRect(
    {
      x: target.kind === "home" ? 180 : 240,
      y: 48,
      width: target.kind === "home" ? 900 : 760,
      height: target.kind === "home" ? 680 : 580,
    },
    { x: 0, y: 0, width: window.innerWidth, height: Math.max(220, window.innerHeight - 132) },
  );
  return {
    expectedWorkloadId: target.expectedWorkloadId,
    expectedWorkloadGeneration: target.expectedWorkloadGeneration,
    id,
    sharedWindowId: item.id,
    projectId: target.projectId,
    kind: target.kind as WorkOSWindow["kind"],
    appId: resource.case === "appInstanceId" ? resource.value : target.kind,
    title: TITLES[target.kind] ?? target.kind,
    rect,
    restoreRect: rect,
    mode: "normal",
    zIndex: 0,
    ...(resource.case === "workloadId" ? { workloadId: resource.value } : {}),
    ...(resource.case === "previewId" ? { previewId: resource.value } : {}),
    ...(resource.case === "sessionId" ? { sessionId: resource.value } : {}),
    ...(resource.case === "artifactId"
      ? { artifact: { artifactId: resource.value, projectId: target.projectId } }
      : {}),
    ...(surface
      ? {
          surface: {
            surfaceSessionId: surface.id,
            url: surface.url,
            projectId: surface.projectId,
            renderer:
              surface.renderer === SurfaceRenderer.DECLARATIVE
                ? "declarative"
                : SurfaceRenderer[surface.renderer],
          },
        }
      : {}),
  };
}
