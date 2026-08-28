import { Code, ConnectError } from "@connectrpc/connect";
import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type SyntheticEvent,
} from "react";
import { AgentTimeline } from "@workos/agent-center";
import { createWorkOSClients, type WorkOSClients } from "@workos/agent-sdk";
import type {
  AgentEvent,
  AgentTask,
  GetHarnessCatalogResponse,
  Project,
  SurfaceSession,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import { initialWindowState, windowReducer } from "@workos/window-manager";
import { AppLibrary } from "./AppLibrary.js";
import { AppSurface, type SurfaceBridgeCredentials } from "./AppSurface.js";
import { HarnessSettings, type CatalogState } from "./HarnessSettings.js";
import { selectionFromProject, taskStatus, type HarnessSelection } from "./model.js";

const clients = createWorkOSClients(window.location.origin);

// The last active project survives a page reload so returning to the desktop
// restores the context the user was working in. Storage failures (private
// modes, embedded webviews) degrade to the previous first-project behavior.
const ACTIVE_PROJECT_STORAGE_KEY = "workos.activeProjectId";

function readStoredActiveProjectId(): string | undefined {
  try {
    return window.sessionStorage.getItem(ACTIVE_PROJECT_STORAGE_KEY) ?? undefined;
  } catch {
    return undefined;
  }
}

export function Desktop({ workosClients = clients }: { workosClients?: WorkOSClients } = {}) {
  const [projects, setProjects] = useState<Project[]>([]);
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
  const bindingOperationsRef = useRef<{ generation: number; tokens: Record<string, number> }>({
    generation: 0,
    tokens: {},
  });
  const [windows, dispatch] = useReducer(windowReducer, initialWindowState);
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

  useEffect(() => {
    if (!activeProjectId) return;
    try {
      window.sessionStorage.setItem(ACTIVE_PROJECT_STORAGE_KEY, activeProjectId);
    } catch {
      // Selection persistence is best-effort; the in-memory selection stays.
    }
  }, [activeProjectId]);

  const refreshProjects = useCallback(async () => {
    const response = await workosClients.projects.listProjects({
      page: { pageSize: 100, pageToken: "" },
    });
    const listed = response.projects;
    // The persisted selection may sit beyond the first project page; fetch
    // it directly so a reload keeps the user's project active instead of
    // silently switching to the oldest listed one.
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
        rect: { x: 0, y: 0, width: 620, height: 520 },
        mode: "normal",
      },
    });
  }, []);

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
        rect: { x: 0, y: 0, width: 900, height: 620 },
        mode: "normal",
      },
    });
  }

  // Closing a window closes its server session best-effort; Core's active-
  // installation revalidation remains the final authority either way.
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
    dispatch({ type: "close", id: windowId });
  }

  // Uninstalling an app closes its windows so a removed instance leaves no
  // orphan surfaces behind.
  function installationRemoved(installationId: string) {
    for (const item of windows.windows) {
      if (item.kind === "app-surface" && item.appId === installationId) {
        closeWindow(item.id);
      }
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

  async function submitTask(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const goal = formString(form, "goal");
    if (!goal || !activeProjectId) return;
    setEvents([]);
    setTask(undefined);
    setError(undefined);
    try {
      const response = await workosClients.agentTasks.submitTask({
        idempotencyKey: crypto.randomUUID(),
        input: {
          targetScope: { scope: { case: "projectId", value: activeProjectId } },
          role: "general",
          goal,
          contextRefs: [],
          requestedCapabilities: [],
          outputArtifactTypes: [],
          parentTaskId: "",
          incidentId: "",
        },
      });
      if (!response.task) return;
      setTask(response.task);
      formElement.reset();
      for await (const item of workosClients.agentTasks.watchTaskEvents({
        taskId: response.task.id,
        afterSequence: 0n,
      })) {
        const received = item.event;
        if (received) setEvents((current) => [...current, received]);
      }
      const latest = await workosClients.agentTasks.getTask({ taskId: response.task.id });
      if (latest.task) setTask(latest.task);
    } catch (reason) {
      setError(asMessage(reason));
    }
  }

  const status = useMemo(() => {
    if (task) return taskStatus(task.state);
    return loading ? "connecting" : "idle";
  }, [loading, task]);

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
              workosClients={workosClients}
              onProjectRefreshed={replaceProject}
              onSurfaceOpened={surfaceOpened}
              onInstallationRemoved={installationRemoved}
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
            style={{ zIndex: windowState.zIndex }}
          >
            <header>
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
            {windowState.kind === "app-surface" && windowState.surface ? (
              <AppSurface
                surface={windowState.surface}
                bridge={bridgeCredentialsRef.current.get(windowState.surface.surfaceSessionId)}
                appBridge={workosClients.appBridge}
              />
            ) : (
              <div className="agent-center-body">
                <form className="task-composer" onSubmit={(event) => void submitTask(event)}>
                  <textarea
                    aria-label="Agent goal"
                    name="goal"
                    placeholder="Ask the current project agent…"
                    disabled={!activeProjectId}
                  />
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
                <AgentTimeline events={events} />
              </div>
            )}
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
  return reason instanceof Error ? reason.message : "Unknown WorkOS error";
}

function formString(form: FormData, name: string): string {
  const value = form.get(name);
  return typeof value === "string" ? value.trim() : "";
}
