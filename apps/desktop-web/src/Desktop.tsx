import { Code, ConnectError } from "@connectrpc/connect";
import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type SyntheticEvent,
} from "react";
import { AgentTimeline } from "@workos/agent-center";
import {
  DOCK_APP_INSTANCE_LIMIT,
  RECENT_APP_INSTANCE_LIMIT,
  createLayoutStore,
  protoFromDeviceClass,
  pushRecentId,
  useDeviceLayout,
  type DeviceLayoutState,
} from "@workos/adaptive-shell";
import { createWorkOSClients, type WorkOSClients } from "@workos/agent-sdk";
import type { DeviceAuthClient } from "@workos/device-auth";
import type {
  AgentEvent,
  AgentTask,
  GetHarnessCatalogResponse,
  Project,
  SurfaceSession,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import { initialWindowState, windowReducer, type WorkOSWindow } from "@workos/window-manager";
import { AdaptiveShell, type SystemWindowId } from "./AdaptiveShell.js";
import { ArtifactCenter } from "./ArtifactCenter.js";
import { ArtifactViewerWindow } from "./ArtifactViewerWindow.js";
import { AppLibrary, openInstallationSurface } from "./AppLibrary.js";
import { ApprovalsView } from "./ApprovalsView.js";
import { AppSurface, type SurfaceBridgeCredentials } from "./AppSurface.js";
import { DeviceCenter } from "./DeviceCenter.js";
import { HarnessSettings, type CatalogState } from "./HarnessSettings.js";
import { KnowledgeCenter, type KnowledgeHit } from "./KnowledgeCenter.js";
import { SystemMonitor } from "./SystemMonitor.js";
import { UsageView } from "./UsageView.js";
import { selectionFromProject, taskStatus, type HarnessSelection } from "./model.js";

const clients = createWorkOSClients(window.location.origin);

// One device-local layout store per app instance: origin-scoped IndexedDB
// with an in-memory fallback. It only ever holds bounded UI references
// (canonical IDs and preferences), never tokens, credentials, or content.
const layoutStore = createLayoutStore();

// The last active project survives a page reload so returning to the desktop
// restores the context the user was working in. Storage failures (private
// modes, embedded webviews) degrade to the previous first-project behavior.
const ACTIVE_PROJECT_STORAGE_KEY = "workos.activeProjectId";

// The Agent Center window keeps three views inside one window; no view is a
// permanent side panel (docs/structure.md 11.5).
type AgentView = "tasks" | "approvals" | "usage";

// One pinned Agent context chip. The digest is kept in memory for the exact
// submit request; the chip label itself shows only the title and type.
interface ContextChip {
  id: string;
  digest: string;
  title: string;
  artifactType: string;
}

const MAX_CONTEXT_CHIPS = 4;

const AGENT_VIEWS: Array<[AgentView, string]> = [
  ["tasks", "Tasks"],
  ["approvals", "Approvals"],
  ["usage", "Usage"],
];

function readStoredActiveProjectId(): string | undefined {
  try {
    return window.sessionStorage.getItem(ACTIVE_PROJECT_STORAGE_KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

export function Desktop({
  workosClients = clients,
  deviceAuth,
}: {
  workosClients?: WorkOSClients;
  deviceAuth?: DeviceAuthClient;
} = {}) {
  const [projects, setProjects] = useState<Project[]>([]);
  // True only after a complete successful page walk. Layout sweeps must not
  // treat the initial empty array or an unavailable Project service as
  // authoritative server truth.
  const [projectsAuthoritative, setProjectsAuthoritative] = useState(false);
  const [activeProjectId, setActiveProjectId] = useState<string | undefined>(
    readStoredActiveProjectId,
  );
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [task, setTask] = useState<AgentTask>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [catalog, setCatalog] = useState<GetHarnessCatalogResponse>();
  const [catalogState, setCatalogState] = useState<CatalogState>("loading");
  const [catalogError, setCatalogError] = useState<string>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [bindingSaving, setBindingSaving] = useState<Record<string, boolean>>({});
  const [bindingEditor, setBindingEditor] = useState<BindingEditor>();
  const [editorProjectId, setEditorProjectId] = useState<string | undefined>(activeProjectId);
  const activeProjectIdRef = useRef<string | undefined>(activeProjectId);
  const taskGenerationRef = useRef(0);
  const taskAbortRef = useRef<AbortController | undefined>(undefined);
  const bindingOperationsRef = useRef<{ generation: number; tokens: Record<string, number> }>({
    generation: 0,
    tokens: {},
  });
  const [windows, dispatch] = useReducer(windowReducer, initialWindowState);
  const [agentView, setAgentView] = useState<AgentView>("tasks");
  // Pinned Agent context chips (ADR-0010): title + artifact type only. The
  // digest and id travel in the submit request — never into a DOM data
  // attribute, URL, storage, or the iframe bridge.
  const [contextChips, setContextChips] = useState<ContextChip[]>([]);
  const [contextHint, setContextHint] = useState<string>();
  // Live app-surface sessions, mirrored from the window state so the unmount
  // cleanup never reads a stale closure of `windows`. Best-effort closes are
  // idempotent server-side, so a rare duplicate call is harmless; the ref is
  // maintained explicitly at open/close points to avoid re-closing sessions
  // that project switches or window closes already dismissed.
  const openSurfaceSessionsRef = useRef<{ surfaceSessionId: string }[]>([]);
  // Bridge credentials live only here — a plain ref the trusted host reads.
  // They never enter window-manager state (which is serializable), React
  // state, the DOM, or any log: the iframe receives a MessagePort, never the
  // token behind it.
  const bridgeCredentialsRef = useRef<Map<string, SurfaceBridgeCredentials>>(new Map());
  const activeProject = projects.find((project) => project.id === activeProjectId);
  activeProjectIdRef.current = activeProjectId;

  // The adaptive device layout: derived by the shared contract from the
  // live viewport (plus fold segments when the host exposes them). The
  // expanded desktop keeps its exact free-window behavior; every other mode
  // renders the adaptive shell.
  const deviceLayout = useDeviceLayout();
  const adaptive = deviceLayout.mode !== "expanded";
  const [deviceLayoutState, setDeviceLayoutState] = useState<DeviceLayoutState>();
  const layoutGenerationRef = useRef(0);

  // recordLayout persists one bounded mutation to the current project +
  // device-class record. Expanded never writes: desktop geometry must not
  // overwrite phone/tablet state.
  const recordLayout = useCallback(
    (mutate: (state: DeviceLayoutState) => DeviceLayoutState) => {
      const projectId = activeProjectIdRef.current;
      if (!projectId || !adaptive) return;
      const generation = layoutGenerationRef.current;
      void layoutStore
        .update(deviceLayout.deviceClass, projectId, new Date().toISOString(), mutate)
        .then((state) => {
          if (generation !== layoutGenerationRef.current) return;
          setDeviceLayoutState(state);
        })
        .catch(() => undefined);
    },
    [adaptive, deviceLayout.deviceClass],
  );

  // Switching Projects discards the previous editor synchronously during the
  // render that changes the active Project, so an unsaved draft or stale
  // feedback is never painted, not even for one frame, and never restored.
  if (editorProjectId !== activeProjectId) {
    setEditorProjectId(activeProjectId);
    setBindingEditor(undefined);
  }
  const activeEditor = bindingEditor?.projectId === activeProject?.id ? bindingEditor : undefined;
  const bindingDraft: HarnessSelection = activeEditor
    ? activeEditor.draft
    : activeProject
      ? selectionFromProject(activeProject)
      : { kind: "global" };

  // A project switch invalidates the pinned context set: stale chips from
  // another project can never ride into a new project's composer.
  useEffect(() => {
    setContextChips([]);
    setContextHint(undefined);
  }, [activeProjectId]);
  useEffect(() => {
    if (!activeProjectId) return;
    try {
      window.sessionStorage.setItem(ACTIVE_PROJECT_STORAGE_KEY, activeProjectId);
    } catch {
      // Selection persistence is best-effort; the in-memory selection stays.
    }
  }, [activeProjectId]);

  // Task/timeline state belongs to exactly one Project. A switch invalidates
  // every outstanding submit/stream continuation before clearing the old
  // snapshot, so a late Project-A event cannot paint into Project B.
  useEffect(() => {
    taskAbortRef.current?.abort();
    taskAbortRef.current = undefined;
    taskGenerationRef.current += 1;
    setEvents([]);
    setTask(undefined);
    setError(undefined);
    return () => {
      taskAbortRef.current?.abort();
    };
  }, [activeProjectId]);

  const refreshProjects = useCallback(async () => {
    const listed: Project[] = [];
    let token = "";
    for (;;) {
      const response = await workosClients.projects.listProjects({
        page: { pageSize: 100, pageToken: token },
      });
      listed.push(...response.projects);
      token = response.page?.nextPageToken ?? "";
      if (token === "") break;
    }
    // A persisted selection should be in the complete walk. Keep the direct
    // read as a conservative compatibility fallback if an older server
    // returns an incomplete page contract.
    const stored = activeProjectIdRef.current;
    if (stored && !listed.some((project) => project.id === stored)) {
      try {
        const fetched = await workosClients.projects.getProject({ projectId: stored });
        if (fetched.project) {
          listed.push(fetched.project);
        }
      } catch {
        // A stored project that no longer resolves falls back to the first
        // listed project.
      }
    }
    setProjects(listed);
    setProjectsAuthoritative(true);
    setActiveProjectId((current) =>
      current && listed.some((project) => project.id === current) ? current : listed[0]?.id,
    );
  }, [workosClients]);

  const refreshCatalog = useCallback(async () => {
    setCatalogState("loading");
    setCatalogError(undefined);
    try {
      const response = await workosClients.harnessCatalog.getHarnessCatalog({});
      setCatalog(response);
      setCatalogState("ready");
    } catch {
      setCatalog(undefined);
      setCatalogState("error");
      setCatalogError("Provider catalog is temporarily unavailable.");
    }
  }, [workosClients]);

  useEffect(() => {
    void refreshProjects()
      .catch((reason: unknown) => {
        setError(asMessage(reason));
      })
      .finally(() => {
        setLoading(false);
      });
  }, [refreshProjects]);

  useEffect(() => {
    void refreshCatalog();
  }, [refreshCatalog]);

  useEffect(() => {
    dispatch({
      type: "open",
      window: {
        id: "agent-center",
        appId: "agent-center",
        title: "Agent Center",
        kind: "agent-center",
        rect: { x: 386, y: 28, width: 620, height: 560 },
        mode: "normal",
      },
    });
  }, []);

  // System Monitor is a normal, non-permanent window opened from the dock:
  // closing it never affects supervision, and an unreachable reliability
  // upstream only degrades this window.
  const openSystemMonitor = useCallback(() => {
    dispatch({
      type: "open",
      window: {
        id: "system-monitor",
        appId: "system-monitor",
        title: "System Monitor",
        kind: "system-monitor",
        rect: { x: 120, y: 80, width: 560, height: 420 },
        mode: "normal",
      },
    });
    recordLayout((state) => ({ ...state, activeSystemWindow: "system-monitor" }));
  }, [recordLayout]);

  // Device Center is a normal window: closing it never affects the device
  // session, and the pairing ticket it shows lives only while it is open.
  const openDeviceCenter = useCallback(() => {
    dispatch({
      type: "open",
      window: {
        id: "device-center",
        appId: "device-center",
        title: "Device Center",
        kind: "device-center",
        rect: { x: 700, y: 90, width: 560, height: 480 },
        mode: "normal",
      },
    });
    recordLayout((state) => ({ ...state, activeSystemWindow: "device-center" }));
  }, [recordLayout]);

  // Artifact Center is a normal, closable window listing the active
  // project's review artifacts. The reducer dedupes on the id, so repeated
  // dock clicks focus the existing window.
  const openArtifactCenter = useCallback(() => {
    if (!activeProjectId) return;
    dispatch({
      type: "open",
      window: {
        id: "artifact-center",
        appId: "artifact-center",
        title: "Artifact Center",
        kind: "artifact-center",
        rect: { x: 120, y: 120, width: 520, height: 480 },
        mode: "normal",
      },
    });
    recordLayout((state) => ({ ...state, activeSystemWindow: "artifact-center" }));
  }, [activeProjectId, recordLayout]);

  // Knowledge Center is a normal, closable window over the active project's
  // lexical projection. Like the Artifact Center it remounts per project and
  // never injects Agent context by itself.
  const openKnowledgeCenter = useCallback(() => {
    if (!activeProjectId) return;
    dispatch({
      type: "open",
      window: {
        id: "knowledge-center",
        appId: "knowledge-center",
        title: "Knowledge Center",
        kind: "knowledge-center",
        rect: { x: 140, y: 140, width: 560, height: 500 },
        mode: "normal",
      },
    });
    recordLayout((state) => ({ ...state, activeSystemWindow: "knowledge-center" }));
  }, [activeProjectId, recordLayout]);

  // Opening one artifact opens (or focuses) exactly one viewer window keyed
  // on the artifact id. The window fetches authoritative content itself; a
  // foreign or vanished reference renders the fixed unavailable verdict.
  const openArtifactViewer = useCallback(
    (artifactId: string, projectId: string) => {
      dispatch({
        type: "open",
        window: {
          id: `artifact-viewer-${artifactId}`,
          appId: "artifact-viewer",
          title: "Artifact Review",
          kind: "artifact-viewer",
          artifact: { artifactId, projectId },
          rect: { x: 320, y: 150, width: 640, height: 520 },
          mode: "normal",
        },
      });
      recordLayout((state) => ({ ...state, activeArtifactId: artifactId }));
    },
    [recordLayout],
  );

  // Timeline artifact events open the same authoritative viewer path; the
  // project fact is the active project, and the server re-checks ownership
  // on the read regardless.
  const openArtifactById = useCallback(
    (artifactId: string) => {
      openArtifactViewer(artifactId, activeProjectId ?? "");
    },
    [activeProjectId, openArtifactViewer],
  );

  useEffect(() => {
    // Unmount invalidates every in-flight binding operation so a pending
    // Promise that settles afterwards cannot touch any binding state.
    return () => {
      bindingOperationsRef.current.generation += 1;
    };
  }, []);

  // Leaving the Desktop entirely makes its still-open app surfaces inert:
  // each one gets a best-effort Close (idempotent server-side) instead of
  // waiting out the session TTL. Page crash or network loss still relies on
  // the TTL and per-request Core revalidation; no unload RPC is guaranteed.
  useEffect(() => {
    return () => {
      for (const item of openSurfaceSessionsRef.current) {
        bridgeCredentialsRef.current.delete(item.surfaceSessionId);
        void workosClients.surfaces
          .closeSurface({ surfaceSessionId: item.surfaceSessionId })
          .catch(() => undefined);
      }
    };
    // The cleanup runs exactly once per Desktop lifetime; the ref always
    // carries the live session set, and the clients identity is stable.
  }, [workosClients]);

  // Switching projects closes the previous project's app windows; their
  // sessions are closed best-effort and the backend keeps failing closed.
  useEffect(() => {
    if (!activeProjectId) return;
    for (const item of windows.windows) {
      if (
        item.kind === "artifact-viewer" &&
        item.artifact &&
        item.artifact.projectId !== activeProjectId
      ) {
        // Review windows belong to the project whose artifact they show;
        // switching projects never carries the previous project's content.
        dispatch({ type: "close", id: item.id });
        continue;
      }
      if (item.kind === "artifact-center" || item.kind === "knowledge-center") {
        // The centers remount keyed on the active project anyway; close them
        // so the previous project's list/results never linger behind a new
        // one.
        dispatch({ type: "close", id: item.id });
        continue;
      }
      if (
        item.kind === "app-surface" &&
        item.surface &&
        item.surface.projectId !== activeProjectId
      ) {
        openSurfaceSessionsRef.current = openSurfaceSessionsRef.current.filter(
          (open) => open.surfaceSessionId !== item.surface?.surfaceSessionId,
        );
        bridgeCredentialsRef.current.delete(item.surface.surfaceSessionId);
        void workosClients.surfaces
          .closeSurface({ surfaceSessionId: item.surface.surfaceSessionId })
          .catch(() => undefined);
        dispatch({ type: "close", id: item.id });
      }
    }
    // Deliberately driven by project switches only: user-initiated closes go
    // through closeWindow, and re-running for window changes would loop.
  }, [activeProjectId]);

  // Device-local layout state (docs/structure.md 12.6): one record per
  // project + device class, loaded generation-guarded so a project switch
  // or a device-class change makes late loads inert.
  useEffect(() => {
    if (!adaptive || !activeProjectId) {
      setDeviceLayoutState(undefined);
      return;
    }
    const generation = ++layoutGenerationRef.current;
    let cancelled = false;
    void layoutStore
      .load(deviceLayout.deviceClass, activeProjectId, new Date().toISOString())
      .then((state) => {
        if (cancelled || generation !== layoutGenerationRef.current) return;
        setDeviceLayoutState(state);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [adaptive, activeProjectId, deviceLayout.deviceClass]);

  // The record only ever holds references that still exist: this sweep
  // drops records for archived or foreign projects whenever the
  // authoritative project list changes (covers archive and logout drift).
  useEffect(() => {
    if (!adaptive || !projectsAuthoritative) return;
    void layoutStore.sweep(new Set(projects.map((project) => project.id))).catch(() => undefined);
  }, [adaptive, projects, projectsAuthoritative]);

  async function createProject(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const name = formString(form, "name");
    if (!name) return;
    setError(undefined);
    try {
      const response = await workosClients.projects.createProject({
        idempotencyKey: crypto.randomUUID(),
        name,
        icon: "◈",
        workspaceRefs: [],
      });
      await refreshProjects();
      const created = response.project;
      if (created) {
        // listProjects is page-limited, so a fresh Project can be missing
        // from the refreshed first page; upsert keeps it selectable.
        setProjects((current) =>
          current.some((candidate) => candidate.id === created.id)
            ? current
            : current.concat(created),
        );
        setActiveProjectId(created.id);
      }
      formElement.reset();
    } catch (reason) {
      setError(asMessage(reason));
    }
  }

  function replaceProject(project: Project) {
    setProjects((current) =>
      current.map((candidate) => (candidate.id === project.id ? project : candidate)),
    );
  }

  // A newly created surface opens one App window. Windows key on the surface
  // session, so a repeated open of the same session can never duplicate it.
  // The device-local layout record learns the active instance and the
  // recency lists (bounded canonical IDs only).
  function surfaceOpened(session: SurfaceSession) {
    openSurfaceSessionsRef.current = openSurfaceSessionsRef.current.concat({
      surfaceSessionId: session.id,
    });
    if (session.bridgeToken !== "") {
      bridgeCredentialsRef.current.set(session.id, {
        token: session.bridgeToken,
        capabilities: [...session.bridgeCapabilities],
      });
    }
    recordLayout((state) => ({
      ...state,
      activeAppInstanceId: session.appInstanceId,
      recentAppInstanceIds: pushRecentId(
        state.recentAppInstanceIds,
        session.appInstanceId,
        RECENT_APP_INSTANCE_LIMIT,
      ),
      dockAppInstanceIds: pushRecentId(
        state.dockAppInstanceIds,
        session.appInstanceId,
        DOCK_APP_INSTANCE_LIMIT,
      ),
    }));
    dispatch({
      type: "open",
      window: {
        id: `surface-${session.id}`,
        appId: session.appInstanceId,
        title: "App",
        kind: "app-surface",
        surface: {
          surfaceSessionId: session.id,
          url: session.url,
          projectId: session.projectId,
        },
        // The app window opens over the launch area, beside — not on top of —
        // the Agent Center window, so approvals and usage stay reachable while
        // an app task runs. The header drag lets the user rearrange further.
        rect: { x: 40, y: 300, width: 656, height: 560 },
        mode: "normal",
      },
    });
  }

  // Closing a window closes its server session best-effort; Core's active-
  // installation revalidation remains the final authority either way. The
  // device-local record drops the closed window's active reference so a
  // stale id cannot linger after the fact.
  function closeWindow(windowId: string) {
    const target = windows.windows.find((item) => item.id === windowId);
    if (target?.kind === "app-surface" && target.surface) {
      openSurfaceSessionsRef.current = openSurfaceSessionsRef.current.filter(
        (item) => item.surfaceSessionId !== target.surface?.surfaceSessionId,
      );
      bridgeCredentialsRef.current.delete(target.surface.surfaceSessionId);
      void workosClients.surfaces
        .closeSurface({ surfaceSessionId: target.surface.surfaceSessionId })
        .catch(() => undefined);
    }
    if (adaptive && target) {
      if (target.kind === "app-surface") {
        recordLayout((state) => ({
          ...state,
          activeAppInstanceId:
            state.activeAppInstanceId === target.appId ? undefined : state.activeAppInstanceId,
        }));
      } else if (target.kind === "artifact-viewer" && target.artifact) {
        recordLayout((state) => ({
          ...state,
          activeArtifactId:
            state.activeArtifactId === target.artifact?.artifactId
              ? undefined
              : state.activeArtifactId,
        }));
      } else if (
        target.kind === "system-monitor" ||
        target.kind === "device-center" ||
        target.kind === "artifact-center" ||
        target.kind === "agent-center"
      ) {
        recordLayout((state) => ({
          ...state,
          activeSystemWindow:
            state.activeSystemWindow === target.id ? undefined : state.activeSystemWindow,
        }));
      }
    }
    dispatch({ type: "close", id: windowId });
  }

  // beginWindowDrag starts a header drag for one managed window. The
  // reducer owns the geometry; the drag previews via inline style and commits
  // the translated rect on mouseup. Traffic lights never start a drag.
  function beginWindowDrag(event: ReactMouseEvent<HTMLElement>, windowId: string) {
    if ((event.target as HTMLElement).closest(".traffic-lights")) return;
    const target = event.currentTarget;
    const startX = event.clientX;
    const startY = event.clientY;
    const box = target.getBoundingClientRect();
    const originX = box.left;
    const originY = box.top;
    let lastX = originX;
    let lastY = originY;
    const onMove = (move: MouseEvent) => {
      lastX = originX + (move.clientX - startX);
      lastY = originY + (move.clientY - startY);
      target.style.left = `${String(lastX)}px`;
      target.style.top = `${String(lastY)}px`;
    };
    const onUp = () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
      dispatch({ type: "move", id: windowId, x: lastX, y: lastY });
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  // A server-confirmed install security-fact change (uninstall, grants, or
  // pinned version) closes its windows so no stale Surface stays visible,
  // and every device-class layout record of this browser profile drops its
  // references to that now-invalid instance.
  function invalidateInstallationReferences(installationId: string) {
    closeInstallationWindows(installationId);
    const projectId = activeProjectIdRef.current;
    const deviceClass = deviceLayout.deviceClass;
    void layoutStore
      .pruneAppInstance(installationId)
      .then(() => {
        if (!projectId || !adaptive) return;
        return layoutStore.load(deviceClass, projectId, new Date().toISOString()).then((state) => {
          if (projectId !== activeProjectIdRef.current) return;
          setDeviceLayoutState(state);
        });
      })
      .catch(() => undefined);
  }

  const installationRemoved = invalidateInstallationReferences;

  // A server-confirmed grant change invalidates every still-open surface of
  // exactly that installation (the backend already denies their bridge calls
  // by grant revision); closing the windows is the best-effort local teardown
  // so the user reopens with the new capabilities.
  function closeInstallationWindows(installationId: string) {
    for (const item of windows.windows) {
      if (item.kind === "app-surface" && item.appId === installationId) {
        closeWindow(item.id);
      }
    }
  }

  // --- Adaptive shell callbacks (compact/medium/fold-separated). The shell
  // renders one main pane; these keep the same window/dispatch semantics as
  // the expanded desktop, so no behavior is duplicated per mode.

  const openAdaptiveSystemWindow = useCallback(
    (id: SystemWindowId) => {
      if (id === "system-monitor") openSystemMonitor();
      else if (id === "device-center") openDeviceCenter();
      else if (id === "artifact-center") openArtifactCenter();
      else if (id === "knowledge-center") openKnowledgeCenter();
      const existing = windows.windows.some((item) => item.id === id);
      if (existing) dispatch({ type: "focus", id });
      recordLayout((state) => ({ ...state, activeSystemWindow: id }));
    },
    [
      openArtifactCenter,
      openDeviceCenter,
      openKnowledgeCenter,
      openSystemMonitor,
      recordLayout,
      windows.windows,
    ],
  );

  const focusAdaptiveWindow = useCallback(
    (windowId: string) => {
      dispatch({ type: "focus", id: windowId });
      const target = windows.windows.find((item) => item.id === windowId);
      if (!target) return;
      if (target.kind === "app-surface") {
        recordLayout((state) => ({
          ...state,
          activeAppInstanceId: target.appId,
          recentAppInstanceIds: pushRecentId(
            state.recentAppInstanceIds,
            target.appId,
            RECENT_APP_INSTANCE_LIMIT,
          ),
        }));
      } else if (target.kind === "artifact-viewer" && target.artifact) {
        recordLayout((state) => ({ ...state, activeArtifactId: target.artifact?.artifactId }));
      } else {
        recordLayout((state) => ({ ...state, activeSystemWindow: target.id }));
      }
    },
    [recordLayout, windows.windows],
  );

  const openAdaptiveAppInstance = useCallback(
    (appInstanceId: string) => {
      const projectId = activeProjectIdRef.current;
      if (!projectId) return;
      void openInstallationSurface(
        workosClients,
        projectId,
        appInstanceId,
        protoFromDeviceClass(deviceLayout.deviceClass),
        () => activeProjectIdRef.current === projectId,
        surfaceOpened,
        () => undefined,
      );
    },
    [deviceLayout.deviceClass, workosClients],
  );

  async function createProjectNamed(name: string) {
    setError(undefined);
    try {
      const response = await workosClients.projects.createProject({
        idempotencyKey: crypto.randomUUID(),
        name,
        icon: "◈",
        workspaceRefs: [],
      });
      const created = response.project;
      if (created) {
        setProjects((current) =>
          current.some((candidate) => candidate.id === created.id)
            ? current
            : current.concat(created),
        );
        setActiveProjectId(created.id);
      }
    } catch (reason) {
      setError(asMessage(reason));
    }
  }

  async function saveHarnessBinding(projectId: string, draft: HarnessSelection) {
    const project = projects.find((candidate) => candidate.id === projectId);
    if (!project || bindingSaving[projectId]) return;
    const token = (bindingOperationsRef.current.tokens[projectId] ?? 0) + 1;
    bindingOperationsRef.current.tokens[projectId] = token;
    const generation = bindingOperationsRef.current.generation;
    const isLive = () =>
      bindingOperationsRef.current.generation === generation &&
      bindingOperationsRef.current.tokens[projectId] === token;
    const isActive = () => activeProjectIdRef.current === projectId;
    setBindingSaving((current) => ({ ...current, [projectId]: true }));
    setBindingEditor((current) =>
      current?.projectId === projectId ? { ...current, feedback: undefined } : current,
    );
    try {
      const response = await workosClients.projectHarnessBindings.setProjectHarnessBinding({
        projectId,
        expectedRevision: project.revision,
        selection:
          draft.kind === "provider"
            ? { case: "providerId", value: draft.providerId }
            : { case: "useGlobalDefault", value: true },
      });
      const updated = response.project;
      if (!updated) throw new Error("missing project response");
      if (!isLive()) return;
      replaceProject(updated);
      if (isActive()) {
        setBindingEditor({
          projectId,
          draft: selectionFromProject(updated),
          feedback: { text: "Harness setting saved.", isError: false },
        });
      }
    } catch (reason) {
      if (reason instanceof ConnectError && reason.code === Code.Aborted) {
        try {
          const refreshedResponse = await workosClients.projects.getProject({ projectId });
          const refreshed = refreshedResponse.project;
          if (!refreshed) throw new Error("missing refreshed project");
          if (!isLive()) return;
          replaceProject(refreshed);
          if (isActive()) {
            setBindingEditor({
              projectId,
              draft: selectionFromProject(refreshed),
              feedback: {
                text: "Project settings changed elsewhere. The latest revision was loaded.",
                isError: true,
              },
            });
          }
        } catch {
          if (!isLive()) return;
          if (isActive()) {
            setBindingEditor((current) =>
              current?.projectId === projectId
                ? {
                    ...current,
                    feedback: {
                      text: "Project settings changed elsewhere and could not be refreshed.",
                      isError: true,
                    },
                  }
                : current,
            );
          }
        }
      } else {
        if (!isLive()) return;
        if (isActive()) {
          setBindingEditor((current) =>
            current?.projectId === projectId
              ? { ...current, feedback: { text: bindingErrorMessage(reason), isError: true } }
              : current,
          );
        }
      }
    } finally {
      if (isLive()) {
        setBindingSaving((current) => ({ ...current, [projectId]: false }));
      }
    }
  }

  // useAsContext pins one artifact of the active project as Agent context.
  // Duplicate selection is idempotent; the set is capped at four with a
  // fixed, accessible hint; a project switch clears the whole set.
  function useAsContext(artifact: {
    id: string;
    digest?: string | undefined;
    title: string;
    type: string;
  }) {
    setContextHint(undefined);
    if (!artifact.digest) return;
    if (contextChips.some((chip) => chip.id === artifact.id)) return;
    if (contextChips.length >= MAX_CONTEXT_CHIPS) {
      setContextHint("At most 4 artifacts can be pinned as Agent context.");
      return;
    }
    setContextChips((current) => [
      ...current,
      {
        id: artifact.id,
        digest: artifact.digest as string,
        title: artifact.title,
        artifactType: artifact.type,
      },
    ]);
  }

  async function submitTask(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const goal = formString(form, "goal");
    const taskProjectId = activeProjectId;
    if (!goal || !taskProjectId) return;
    taskAbortRef.current?.abort();
    const abortController = new AbortController();
    taskAbortRef.current = abortController;
    const generation = ++taskGenerationRef.current;
    const isLive = () =>
      taskGenerationRef.current === generation && activeProjectIdRef.current === taskProjectId;
    const outputArtifactTypes: string[] = [];
    if (form.get("artifact-markdown") === "on") outputArtifactTypes.push("document.markdown.v1");
    if (form.get("artifact-diff") === "on") outputArtifactTypes.push("code.unified-diff.v1");
    setEvents([]);
    setTask(undefined);
    setError(undefined);
    try {
      const response = await workosClients.agentTasks.submitTask(
        {
          idempotencyKey: crypto.randomUUID(),
          input: {
            targetScope: { scope: { case: "projectId", value: taskProjectId } },
            role: "general",
            goal,
            contextRefs: contextChips.map((chip) => ({
              type: "artifact.review.v1",
              id: chip.id,
              revision: chip.digest,
            })),
            requestedCapabilities: [],
            outputArtifactTypes,
            parentTaskId: "",
            incidentId: "",
          },
        },
        { signal: abortController.signal },
      );
      if (!isLive() || !response.task) return;
      setTask(response.task);
      formElement.reset();
      // Submitted chips are consumed: the request carried their exact
      // id+digest pairs.
      setContextChips([]);
      setContextHint(undefined);
      for await (const item of workosClients.agentTasks.watchTaskEvents(
        {
          taskId: response.task.id,
          afterSequence: 0n,
        },
        { signal: abortController.signal },
      )) {
        if (!isLive()) break;
        const received = item.event;
        if (received) setEvents((current) => [...current, received]);
      }
      if (!isLive()) return;
      const latest = await workosClients.agentTasks.getTask(
        { taskId: response.task.id },
        { signal: abortController.signal },
      );
      if (isLive() && latest.task) setTask(latest.task);
    } catch (reason) {
      if (isLive()) setError(asMessage(reason));
    } finally {
      if (taskAbortRef.current === abortController) {
        taskAbortRef.current = undefined;
      }
    }
  }

  // renderWindowBody renders one window's body. Both the expanded free-window
  // shell and the adaptive panes render exactly these bodies, so behavior
  // never forks per mode.
  function renderWindowBody(windowState: WorkOSWindow) {
    return windowState.kind === "app-surface" && windowState.surface ? (
      <AppSurface
        surface={windowState.surface}
        bridge={bridgeCredentialsRef.current.get(windowState.surface.surfaceSessionId)}
        appBridge={workosClients.appBridge}
      />
    ) : windowState.kind === "system-monitor" ? (
      <SystemMonitor
        expectedProjectRevision={activeProject?.revision}
        onInstallationVersionChanged={invalidateInstallationReferences}
        onProjectRevisionChanged={(revision) => {
          if (activeProject) replaceProject({ ...activeProject, revision });
        }}
        projectId={activeProjectId}
        workosClients={workosClients}
      />
    ) : windowState.kind === "artifact-center" ? (
      activeProject ? (
        <ArtifactCenter
          key={activeProject.id}
          projectId={activeProject.id}
          workosClients={workosClients}
          selectedContextIds={new Set(contextChips.map((chip) => chip.id))}
          onUseAsContext={useAsContext}
          onOpenArtifact={(artifact) => {
            openArtifactViewer(artifact.id, artifact.projectId);
          }}
        />
      ) : (
        <p className="empty-state">Create a project to review its artifacts.</p>
      )
    ) : windowState.kind === "knowledge-center" ? (
      activeProject ? (
        <KnowledgeCenter
          key={activeProject.id}
          projectId={activeProject.id}
          workosClients={workosClients}
          selectedContextIds={new Set(contextChips.map((chip) => chip.id))}
          onUseAsContext={(hit: KnowledgeHit) => {
            useAsContext({
              id: hit.artifactId,
              digest: hit.digest,
              title: hit.title,
              type: hit.artifactType,
            });
          }}
          onOpenArtifact={(artifactId) => {
            openArtifactViewer(artifactId, activeProject.id);
          }}
        />
      ) : (
        <p className="empty-state">Create a project to search its knowledge.</p>
      )
    ) : windowState.kind === "artifact-viewer" && windowState.artifact ? (
      <ArtifactViewerWindow
        artifactId={windowState.artifact.artifactId}
        projectId={windowState.artifact.projectId}
        workosClients={workosClients}
        contextPinned={contextChips.some((chip) => chip.id === windowState.artifact?.artifactId)}
        onUseAsContext={(artifact) => {
          useAsContext({
            id: artifact.id,
            digest: artifact.digest,
            title: artifact.title,
            type: artifact.type,
          });
        }}
      />
    ) : windowState.kind === "device-center" ? (
      deviceAuth ? (
        <DeviceCenter deviceAuth={deviceAuth} onSessionEnded={() => layoutStore.clearAll()} />
      ) : (
        <p className="empty-state">Device management is not available in this deployment.</p>
      )
    ) : (
      <div className="agent-center-body">
        <div className="agent-views" role="tablist" aria-label="Agent Center views">
          {AGENT_VIEWS.map(([view, label]) => (
            <button
              aria-selected={agentView === view}
              className={agentView === view ? "agent-view-tab active" : "agent-view-tab"}
              key={view}
              onClick={() => {
                setAgentView(view);
              }}
              role="tab"
              type="button"
            >
              {label}
            </button>
          ))}
        </div>
        {agentView === "approvals" ? (
          activeProjectId ? (
            <ApprovalsView projectId={activeProjectId} workosClients={workosClients} />
          ) : (
            <p className="empty-state">Create a project to manage approvals.</p>
          )
        ) : null}
        {agentView === "usage" ? (
          activeProjectId ? (
            <UsageView projectId={activeProjectId} workosClients={workosClients} />
          ) : (
            <p className="empty-state">Create a project to see agent usage.</p>
          )
        ) : null}
        {agentView === "tasks" ? (
          <>
            <form className="task-composer" onSubmit={(event) => void submitTask(event)}>
              <textarea
                aria-label="Agent goal"
                name="goal"
                placeholder="Ask the current project agent…"
                disabled={!activeProjectId}
              />
              {contextChips.length > 0 ? (
                <ul className="context-chips" aria-label="Pinned Agent context">
                  {contextChips.map((chip) => (
                    <li key={chip.id} className="context-chip" data-testid="context-chip">
                      <span>{chip.title}</span>
                      <span className="context-chip-type">
                        {chip.artifactType === "document.markdown.v1" ? "Markdown" : "Unified diff"}
                      </span>
                      <button
                        type="button"
                        aria-label={`Remove ${chip.title} from Agent context`}
                        onClick={() => {
                          setContextChips((current) =>
                            current.filter((entry) => entry.id !== chip.id),
                          );
                          setContextHint(undefined);
                        }}
                      >
                        ✕
                      </button>
                    </li>
                  ))}
                </ul>
              ) : null}
              {contextHint ? (
                <p role="status" className="context-hint" data-testid="context-hint">
                  {contextHint}
                </p>
              ) : null}
              <div className="artifact-request" aria-label="Requested artifact outputs">
                <label>
                  <input type="checkbox" name="artifact-markdown" /> Markdown document
                </label>
                <label>
                  <input type="checkbox" name="artifact-diff" /> Unified diff
                </label>
              </div>
              <Button disabled={!activeProjectId} type="submit">
                Run task
              </Button>
            </form>
            {task ? (
              <dl className="task-snapshot" aria-label="Task provider snapshot">
                <dt>Provider snapshot</dt>
                <dd>{task.providerId || "unknown"}</dd>
                <dt>Task</dt>
                <dd>{task.id}</dd>
              </dl>
            ) : null}
            <AgentTimeline events={events} onOpenArtifact={openArtifactById} />
          </>
        ) : null}
      </div>
    );
  }

  const status = useMemo(() => {
    if (task) return taskStatus(task.state);
    return loading ? "connecting" : "idle";
  }, [loading, task]);

  // The adaptive shell (compact / medium / fold-separated) renders the same
  // state through a mode-specific chrome. The expanded branch below keeps
  // the exact free-window desktop, so desktop behavior cannot regress.
  if (adaptive) {
    return (
      <AdaptiveShell
        layout={deviceLayout}
        windows={windows}
        status={status}
        activeProject={activeProject}
        projects={projects}
        layoutState={deviceLayoutState}
        onSwitchProject={setActiveProjectId}
        onCreateProject={(name) => void createProjectNamed(name)}
        onOpenSystemWindow={openAdaptiveSystemWindow}
        onFocusWindow={focusAdaptiveWindow}
        onCloseWindow={closeWindow}
        onLayoutPreference={(preference) => {
          recordLayout((state) => ({ ...state, layoutPreference: preference }));
        }}
        onOpenAppInstance={openAdaptiveAppInstance}
        renderWindowBody={renderWindowBody}
        renderAppLibrary={() =>
          activeProject ? (
            <AppLibrary
              key={activeProject.id}
              project={activeProject}
              deviceClass={protoFromDeviceClass(deviceLayout.deviceClass)}
              workosClients={workosClients}
              onProjectRefreshed={replaceProject}
              onSurfaceOpened={surfaceOpened}
              onInstallationRemoved={installationRemoved}
              onInstallationGrantsChanged={invalidateInstallationReferences}
              onInstallationVersionChanged={invalidateInstallationReferences}
            />
          ) : null
        }
        renderProjectSettings={() =>
          activeProject ? (
            <HarnessSettings
              catalog={catalog}
              catalogError={catalogError}
              catalogState={catalogState}
              draft={bindingDraft}
              feedback={activeEditor?.feedback?.text}
              feedbackIsError={activeEditor?.feedback?.isError}
              project={activeProject}
              saving={bindingSaving[activeProject.id] ?? false}
              onRetry={() => void refreshCatalog()}
              onSave={() => {
                void saveHarnessBinding(activeProject.id, bindingDraft);
              }}
              onSelectionChange={(selection) => {
                setBindingEditor({ projectId: activeProject.id, draft: selection });
              }}
            />
          ) : null
        }
      >
        {error ? (
          <p className="error-toast" role="alert">
            {error}
          </p>
        ) : null}
      </AdaptiveShell>
    );
  }

  return (
    <main className="desktop-shell">
      <header className="system-bar">
        <strong>◈ WorkOS</strong>
        <button className="project-switcher" type="button">
          {activeProject?.name ?? "Global Space"} <span>⌄</span>
        </button>
        <span className="agent-status">
          <i /> Project Agent · {status}
        </span>
      </header>

      <section className="desktop-canvas">
        <aside className="mission-control" aria-label="Projects">
          <p>PROJECT SPACES</p>
          <div className="project-grid">
            {projects.map((project) => (
              <button
                className={project.id === activeProjectId ? "project-card active" : "project-card"}
                key={project.id}
                onClick={() => {
                  setActiveProjectId(project.id);
                }}
                type="button"
              >
                <span>{project.icon || "◌"}</span>
                <strong>{project.name}</strong>
                <small>revision {project.revision.toString()}</small>
              </button>
            ))}
            <form
              className="project-card new-project"
              onSubmit={(event) => void createProject(event)}
            >
              <input
                aria-label="Project name"
                name="name"
                placeholder="New project"
                maxLength={120}
              />
              <Button type="submit">Create space</Button>
            </form>
          </div>
          <Button
            aria-expanded={settingsOpen}
            disabled={!activeProject}
            onClick={() => {
              setSettingsOpen((current) => !current);
            }}
            type="button"
          >
            {settingsOpen ? "Close project settings" : "Project settings"}
          </Button>
          <Button
            aria-expanded={libraryOpen}
            disabled={!activeProject}
            onClick={() => {
              setLibraryOpen((current) => !current);
            }}
            type="button"
          >
            {libraryOpen ? "Close App Library" : "App Library"}
          </Button>
          {/* Keying on the project id remounts the library when the active
              project changes, so lists, feedback, and in-flight operations
              never leak across projects. */}
          {libraryOpen && activeProject ? (
            <AppLibrary
              key={activeProject.id}
              project={activeProject}
              deviceClass={protoFromDeviceClass(deviceLayout.deviceClass)}
              workosClients={workosClients}
              onProjectRefreshed={replaceProject}
              onSurfaceOpened={surfaceOpened}
              onInstallationRemoved={installationRemoved}
              onInstallationGrantsChanged={invalidateInstallationReferences}
              onInstallationVersionChanged={invalidateInstallationReferences}
            />
          ) : null}
          {settingsOpen && activeProject ? (
            <HarnessSettings
              catalog={catalog}
              catalogError={catalogError}
              catalogState={catalogState}
              draft={bindingDraft}
              feedback={activeEditor?.feedback?.text}
              feedbackIsError={activeEditor?.feedback?.isError}
              project={activeProject}
              saving={bindingSaving[activeProject.id] ?? false}
              onRetry={() => void refreshCatalog()}
              onSave={() => {
                void saveHarnessBinding(activeProject.id, bindingDraft);
              }}
              onSelectionChange={(selection) => {
                setBindingEditor({ projectId: activeProject.id, draft: selection });
              }}
            />
          ) : null}
        </aside>

        {windows.windows.map((windowState) => (
          <section
            className={
              windowState.kind === "app-surface" ? "workos-window app-window" : "workos-window"
            }
            key={windowState.id}
            style={{
              zIndex: windowState.zIndex,
              left: windowState.rect.x,
              top: windowState.rect.y,
              width: windowState.rect.width,
              height: windowState.rect.height,
            }}
          >
            <header
              onMouseDown={(event) => {
                dispatch({ type: "focus", id: windowState.id });
                beginWindowDrag(event, windowState.id);
              }}
            >
              <div className="traffic-lights">
                <i />
                <i />
                <button
                  aria-label={`Close ${windowState.title}`}
                  className="traffic-close"
                  onClick={() => {
                    closeWindow(windowState.id);
                  }}
                  type="button"
                />
              </div>
              <strong>{windowState.title}</strong>
              <span>{activeProject?.name ?? "No project"}</span>
            </header>
            {renderWindowBody(windowState)}
          </section>
        ))}

        {error ? (
          <p className="error-toast" role="alert">
            {error}
          </p>
        ) : null}
      </section>

      <nav className="dock" aria-label="WorkOS Dock">
        {["⌂", "A", "◫", "⌘", "▤", "⚙"].map((item, index) => (
          <button type="button" key={`${item}-${String(index)}`}>
            {item}
          </button>
        ))}
        <button
          type="button"
          aria-label="Open System Monitor"
          data-testid="open-system-monitor"
          onClick={openSystemMonitor}
        >
          ⏻
        </button>
        <button
          type="button"
          aria-label="Open Device Center"
          data-testid="open-device-center"
          onClick={openDeviceCenter}
        >
          🔑
        </button>
        <button
          type="button"
          aria-label="Open Artifact Center"
          data-testid="open-artifact-center"
          disabled={!activeProject}
          onClick={openArtifactCenter}
        >
          ☰
        </button>
        <button
          type="button"
          aria-label="Open Knowledge Center"
          data-testid="open-knowledge-center"
          disabled={!activeProject}
          onClick={openKnowledgeCenter}
        >
          ✦
        </button>
      </nav>
    </main>
  );
}

interface BindingFeedback {
  text: string;
  isError: boolean;
}

// The editor belongs to the Project the user is currently viewing; drafts and
// feedback never outlive the visit during which they were produced.
interface BindingEditor {
  projectId: string;
  draft: HarnessSelection;
  feedback?: BindingFeedback | undefined;
}

function bindingErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "Harness setting could not be saved.";
  switch (reason.code) {
    case Code.FailedPrecondition:
      return "That provider cannot be selected right now. Refresh the catalog and try again.";
    case Code.NotFound:
      return "The project no longer exists.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "Provider catalog is temporarily unavailable. Global Default can still be selected.";
    default:
      return "Harness setting could not be saved.";
  }
}

function asMessage(reason: unknown): string {
  if (reason instanceof ConnectError) {
    if (reason.code === Code.Unauthenticated) {
      // A mid-operation session end never replays business mutations: the
      // user re-authenticates and retries explicitly.
      return "Your device session has ended. Sign in again, then retry.";
    }
    return reason.message;
  }
  return reason instanceof Error ? reason.message : "Unknown WorkOS error";
}

function formString(form: FormData, name: string): string {
  const value = form.get(name);
  return typeof value === "string" ? value.trim() : "";
}
