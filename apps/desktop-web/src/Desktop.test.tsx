// @vitest-environment jsdom

import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AgentTaskState,
  HarnessInstancePolicy,
  HealthState,
  type AgentEvent,
  type AgentTask,
  type GetHarnessCatalogResponse,
  type HarnessProviderInfo,
  type Project,
} from "@workos/protocol";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Desktop } from "./Desktop.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(cleanup);

describe("Desktop harness workflow", () => {
  it("reloads the Project after a revision conflict and resets the selection", async () => {
    const first = project("project-1", "Project One", 1n);
    const refreshed = project("project-1", "Project One", 2n, "fake");
    const setBinding = vi.fn(() =>
      Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
    );
    const getProject = vi.fn(() => Promise.resolve({ project: refreshed }));
    render(
      <Desktop workosClients={clientFixture({ projects: [first], setBinding, getProject })} />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
    await userEvent.click(screen.getByRole("radio", { name: "Select DeepSeek Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));

    expect(setBinding).toHaveBeenCalledWith({
      projectId: "project-1",
      expectedRevision: 1n,
      selection: { case: "providerId", value: "deepseek" },
    });
    await waitFor(() => {
      expect(getProject).toHaveBeenCalledWith({ projectId: "project-1" });
    });
    expect(
      await screen.findByText(
        "Project settings changed elsewhere. The latest revision was loaded.",
      ),
    ).toBeTruthy();
    expect(screen.getAllByText("revision 2")).toHaveLength(2);
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
  });

  it("uses the active Project's saved binding when switching Project spaces", async () => {
    render(
      <Desktop
        workosClients={clientFixture({
          projects: [
            project("project-1", "Project One", 3n, "fake"),
            project("project-2", "Project Two", 8n, "deepseek"),
          ],
        })}
      />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    await waitFor(() => {
      expect(
        screen.getByRole<HTMLInputElement>("radio", {
          name: "Select DeepSeek Harness",
        }).checked,
      ).toBe(true);
    });
  });

  it("shows the provider snapshot returned by SubmitTask instead of the Project binding", async () => {
    const submitted = task("task-1", "deepseek", AgentTaskState.RUNNING);
    const completed = task("task-1", "deepseek", AgentTaskState.COMPLETED);
    const started = runStarted("task-1", "deepseek");
    const workosClients = clientFixture({
      projects: [project("project-1", "Project One", 1n, "fake")],
      submitTask: vi.fn(() => Promise.resolve({ task: submitted })),
      watchTaskEvents: vi.fn(() => eventStream([started])),
      getTask: vi.fn(() => Promise.resolve({ task: completed })),
    });
    render(<Desktop workosClients={workosClients} />);

    const goal = await screen.findByRole("textbox", { name: "Agent goal" });
    await userEvent.type(goal, "prove the provider snapshot");
    await userEvent.click(screen.getByRole("button", { name: "Run task" }));

    const snapshot = await screen.findByLabelText("Task provider snapshot");
    expect(within(snapshot).getByText("deepseek")).toBeTruthy();
    expect(await screen.findByText("Run started · deepseek")).toBeTruthy();
    expect(snapshot.textContent).not.toContain("fake");
  });

  it("isolates a pending save from a Project switch so a late success cannot overwrite the other editor", async () => {
    const pending = deferred<{ project: Project }>();
    const setBinding = vi.fn(() => pending.promise);
    render(
      <Desktop
        workosClients={clientFixture({
          projects: [
            project("project-1", "Project One", 1n),
            project("project-2", "Project Two", 8n, "deepseek"),
          ],
          setBinding,
        })}
      />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
    await userEvent.click(screen.getByRole("radio", { name: "Select DeepSeek Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(setBinding).toHaveBeenCalledWith({
      projectId: "project-1",
      expectedRevision: 1n,
      selection: { case: "providerId", value: "deepseek" },
    });

    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select DeepSeek Harness" }).checked,
    ).toBe(true);

    await act(async () => {
      await pending.resolve({ project: project("project-1", "Project One", 2n, "fake") });
    });
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select DeepSeek Harness" }).checked,
    ).toBe(true);
    expect(screen.queryByText("Harness setting saved.")).toBeNull();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Project One revision 2/ })).toBeTruthy();
    });

    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(setBinding).toHaveBeenLastCalledWith({
      projectId: "project-2",
      expectedRevision: 8n,
      selection: { case: "providerId", value: "fake" },
    });
  });

  it("isolates a conflict refresh to its Project while the user edits another Project", async () => {
    const refresh = deferred<{ project: Project }>();
    const setBinding = vi.fn(() =>
      Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
    );
    const getProject = vi.fn(() => refresh.promise);
    render(
      <Desktop
        workosClients={clientFixture({
          projects: [
            project("project-1", "Project One", 1n),
            project("project-2", "Project Two", 8n, "deepseek"),
          ],
          setBinding,
          getProject,
        })}
      />,
    );

    await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
    await userEvent.click(screen.getByRole("radio", { name: "Select DeepSeek Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    await waitFor(() => {
      expect(getProject).toHaveBeenCalledWith({ projectId: "project-1" });
    });

    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    expect(screen.queryByText(/changed elsewhere/)).toBeNull();

    await act(async () => {
      await refresh.resolve({ project: project("project-1", "Project One", 2n, "fake") });
    });
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select DeepSeek Harness" }).checked,
    ).toBe(true);
    expect(screen.queryByText(/changed elsewhere/)).toBeNull();
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /Project One revision 2/ })).toBeTruthy();
    });

    await userEvent.click(screen.getByRole("button", { name: /Project One revision 2/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
    expect(screen.getByText(/changed elsewhere/)).toBeTruthy();
  });
});

function clientFixture({
  projects,
  setBinding = vi.fn(),
  getProject = vi.fn(),
  submitTask = vi.fn(),
  watchTaskEvents = vi.fn(() => eventStream([])),
  getTask = vi.fn(),
}: {
  projects: Project[];
  setBinding?: ReturnType<typeof vi.fn>;
  getProject?: ReturnType<typeof vi.fn>;
  submitTask?: ReturnType<typeof vi.fn>;
  watchTaskEvents?: ReturnType<typeof vi.fn>;
  getTask?: ReturnType<typeof vi.fn>;
}): WorkOSClients {
  return {
    projects: {
      listProjects: vi.fn(() => Promise.resolve({ projects, page: undefined })),
      getProject,
      createProject: vi.fn(),
    },
    projectHarnessBindings: { setProjectHarnessBinding: setBinding },
    harnessCatalog: { getHarnessCatalog: vi.fn(() => Promise.resolve(providerCatalog())) },
    agentTasks: { submitTask, watchTaskEvents, getTask },
  } as unknown as WorkOSClients;
}

async function* eventStream(events: AgentEvent[]) {
  await Promise.resolve();
  for (const event of events) yield { event };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return {
    promise,
    resolve: (value: T) => {
      resolve(value);
      return promise;
    },
    reject,
  };
}

function project(id: string, name: string, revision: bigint, providerId?: string): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id,
    ownerUserId: "local-user",
    name,
    icon: "◈",
    workspaceRefs: [],
    harnessBinding: providerId
      ? {
          $typeName: "workos.project.v1.HarnessBinding",
          providerId,
          instancePolicy: HarnessInstancePolicy.EPHEMERAL,
          profileId: "",
          credentialRef: "",
          resourcePolicyId: "project-no-tools",
        }
      : undefined,
    installedAppIds: [],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    artifactCollectionId: "",
    revision,
  };
}

function task(id: string, providerId: string, state: AgentTaskState): AgentTask {
  return {
    $typeName: "workos.agent.v1.AgentTask",
    id,
    ownerUserId: "local-user",
    state,
    providerId,
    harnessInstanceId: "instance-1",
    runId: "run-1",
    lastEventSequence: 1n,
  };
}

function runStarted(taskId: string, providerId: string): AgentEvent {
  return {
    $typeName: "workos.agent.v1.AgentEvent",
    id: "event-1",
    taskId,
    sequence: 1n,
    event: {
      case: "runStarted",
      value: {
        $typeName: "workos.agent.v1.RunStarted",
        runId: "run-1",
        providerId,
      },
    },
  };
}

function providerCatalog(): GetHarnessCatalogResponse {
  return {
    $typeName: "workos.harness.v1.GetHarnessCatalogResponse",
    defaultProviderId: "fake",
    providers: [provider("fake", "Fake Harness"), provider("deepseek", "DeepSeek Harness")],
  };
}

function provider(id: string, displayName: string): HarnessProviderInfo {
  return {
    $typeName: "workos.harness.v1.HarnessProviderInfo",
    id,
    displayName,
    adapterVersion: "1.0.0",
    health: HealthState.HEALTHY,
    unavailableReason: "",
    capabilities: {
      $typeName: "workos.harness.v1.HarnessCapabilities",
      streaming: true,
      persistentSessions: false,
      resume: false,
      steerDuringRun: false,
      approvals: false,
      toolRegistration: false,
      mcp: false,
      subagents: false,
      workspaceMount: false,
      structuredArtifacts: false,
      usageReporting: true,
    },
  };
}
