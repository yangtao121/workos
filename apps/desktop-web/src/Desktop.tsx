import { useCallback, useEffect, useMemo, useReducer, useState, type SyntheticEvent } from "react";
import { AgentTimeline } from "@workos/agent-center";
import { createWorkOSClients } from "@workos/agent-sdk";
import type { AgentEvent, AgentTask, Project } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import { initialWindowState, windowReducer } from "@workos/window-manager";
import { taskStatus } from "./model.js";

const clients = createWorkOSClients(window.location.origin);

export function Desktop() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [activeProjectId, setActiveProjectId] = useState<string>();
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [task, setTask] = useState<AgentTask>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [windows, dispatch] = useReducer(windowReducer, initialWindowState);
  const activeProject = projects.find((project) => project.id === activeProjectId);

  const refreshProjects = useCallback(async () => {
    const response = await clients.projects.listProjects({
      page: { pageSize: 100, pageToken: "" },
    });
    setProjects(response.projects);
    setActiveProjectId((current) => current ?? response.projects[0]?.id);
  }, []);

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
      const response = await clients.projects.createProject({
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

  async function submitTask(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    const goal = formString(form, "goal");
    if (!goal || !activeProjectId) return;
    setEvents([]);
    setError(undefined);
    try {
      const response = await clients.agentTasks.submitTask({
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
      for await (const item of clients.agentTasks.watchTaskEvents({
        taskId: response.task.id,
        afterSequence: 0n,
      })) {
        const received = item.event;
        if (received) setEvents((current) => [...current, received]);
      }
      const latest = await clients.agentTasks.getTask({ taskId: response.task.id });
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

function asMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : "Unknown WorkOS error";
}

function formString(form: FormData, name: string): string {
  const value = form.get(name);
  return typeof value === "string" ? value.trim() : "";
}
