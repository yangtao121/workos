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
import type { AgentEvent, AgentTask, GetHarnessCatalogResponse, Project } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import { initialWindowState, windowReducer } from "@workos/window-manager";
import { HarnessSettings, type CatalogState } from "./HarnessSettings.js";
import { selectionFromProject, taskStatus, type HarnessSelection } from "./model.js";

const clients = createWorkOSClients(window.location.origin);

export function Desktop({ workosClients = clients }: { workosClients?: WorkOSClients } = {}) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [activeProjectId, setActiveProjectId] = useState<string>();
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [task, setTask] = useState<AgentTask>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [catalog, setCatalog] = useState<GetHarnessCatalogResponse>();
  const [catalogState, setCatalogState] = useState<CatalogState>("loading");
  const [catalogError, setCatalogError] = useState<string>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [bindingDrafts, setBindingDrafts] = useState<Record<string, HarnessSelection>>({});
  const [bindingSaving, setBindingSaving] = useState<Record<string, boolean>>({});
  const [bindingFeedback, setBindingFeedback] = useState<
    Record<string, BindingFeedback | undefined>
  >({});
  const bindingOperationTokens = useRef<Record<string, number>>({});
  const [windows, dispatch] = useReducer(windowReducer, initialWindowState);
  const activeProject = projects.find((project) => project.id === activeProjectId);
  const bindingDraft: HarnessSelection = activeProject
    ? (bindingDrafts[activeProject.id] ?? selectionFromProject(activeProject))
    : { kind: "global" };

  const refreshProjects = useCallback(async () => {
    const response = await workosClients.projects.listProjects({
      page: { pageSize: 100, pageToken: "" },
    });
    setProjects(response.projects);
    setActiveProjectId((current) => current ?? response.projects[0]?.id);
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
        rect: { x: 0, y: 0, width: 620, height: 520 },
        mode: "normal",
      },
    });
  }, []);

  async function createProject(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
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
      if (response.project) setActiveProjectId(response.project.id);
      event.currentTarget.reset();
    } catch (reason) {
      setError(asMessage(reason));
    }
  }

  function replaceProject(project: Project) {
    setProjects((current) =>
      current.map((candidate) => (candidate.id === project.id ? project : candidate)),
    );
  }

  async function saveHarnessBinding(projectId: string) {
    const project = projects.find((candidate) => candidate.id === projectId);
    if (!project || bindingSaving[projectId]) return;
    const token = (bindingOperationTokens.current[projectId] ?? 0) + 1;
    bindingOperationTokens.current[projectId] = token;
    const isLatest = () => bindingOperationTokens.current[projectId] === token;
    setBindingSaving((current) => ({ ...current, [projectId]: true }));
    setBindingFeedback((current) => ({ ...current, [projectId]: undefined }));
    try {
      const response = await workosClients.projectHarnessBindings.setProjectHarnessBinding({
        projectId,
        expectedRevision: project.revision,
        selection:
          bindingDraft.kind === "provider"
            ? { case: "providerId", value: bindingDraft.providerId }
            : { case: "useGlobalDefault", value: true },
      });
      const updated = response.project;
      if (!updated) throw new Error("missing project response");
      replaceProject(updated);
      if (!isLatest()) return;
      setBindingDrafts((current) => ({
        ...current,
        [projectId]: selectionFromProject(updated),
      }));
      setBindingFeedback((current) => ({
        ...current,
        [projectId]: { text: "Harness setting saved.", isError: false },
      }));
    } catch (reason) {
      if (reason instanceof ConnectError && reason.code === Code.Aborted) {
        try {
          const refreshedResponse = await workosClients.projects.getProject({ projectId });
          const refreshed = refreshedResponse.project;
          if (!refreshed) throw new Error("missing refreshed project");
          replaceProject(refreshed);
          if (!isLatest()) return;
          setBindingDrafts((current) => ({
            ...current,
            [projectId]: selectionFromProject(refreshed),
          }));
          setBindingFeedback((current) => ({
            ...current,
            [projectId]: {
              text: "Project settings changed elsewhere. The latest revision was loaded.",
              isError: true,
            },
          }));
        } catch {
          if (!isLatest()) return;
          setBindingFeedback((current) => ({
            ...current,
            [projectId]: {
              text: "Project settings changed elsewhere and could not be refreshed.",
              isError: true,
            },
          }));
        }
      } else {
        if (!isLatest()) return;
        setBindingFeedback((current) => ({
          ...current,
          [projectId]: { text: bindingErrorMessage(reason), isError: true },
        }));
      }
    } finally {
      if (isLatest()) {
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
          {settingsOpen && activeProject ? (
            <HarnessSettings
              catalog={catalog}
              catalogError={catalogError}
              catalogState={catalogState}
              draft={bindingDraft}
              feedback={bindingFeedback[activeProject.id]?.text}
              feedbackIsError={bindingFeedback[activeProject.id]?.isError}
              project={activeProject}
              saving={bindingSaving[activeProject.id] ?? false}
              onRetry={() => void refreshCatalog()}
              onSave={() => void saveHarnessBinding(activeProject.id)}
              onSelectionChange={(selection) => {
                setBindingDrafts((current) => ({
                  ...current,
                  [activeProject.id]: selection,
                }));
                setBindingFeedback((current) => ({
                  ...current,
                  [activeProject.id]: undefined,
                }));
              }}
            />
          ) : null}
        </aside>

        {windows.windows.map((windowState) => (
          <section
            className="workos-window"
            key={windowState.id}
            style={{ zIndex: windowState.zIndex }}
          >
            <header>
              <div className="traffic-lights">
                <i />
                <i />
                <i />
              </div>
              <strong>{windowState.title}</strong>
              <span>{activeProject?.name ?? "No project"}</span>
            </header>
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
