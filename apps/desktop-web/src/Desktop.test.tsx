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
  type SurfaceSession,
} from "@workos/protocol";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Desktop } from "./Desktop.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  cleanup();
  // The desktop persists the active project in sessionStorage across
  // reloads; tests must not inherit a previous test's selection.
  window.sessionStorage.clear();
});

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

    // A's success settled while A was inactive: returning to A shows the
    // response revision/binding from the updated cache, but no stale
    // success feedback.
    await userEvent.click(screen.getByRole("button", { name: /Project One revision 2/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
    expect(screen.getAllByText("revision 2")).toHaveLength(2);
    expect(screen.queryByText("Harness setting saved.")).toBeNull();
  });

  it("discards an unsaved draft and its feedback when leaving and returning to a Project", async () => {
    const setBinding = vi.fn(() =>
      Promise.resolve({ project: project("project-1", "Project One", 2n, "fake") }),
    );
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
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Use Global Default" }).checked,
    ).toBe(true);

    // An unsaved selection must not survive a round-trip through Project Two.
    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    expect(saveButton().disabled).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    await userEvent.click(screen.getByRole("button", { name: /Project One revision 1/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Use Global Default" }).checked,
    ).toBe(true);
    expect(saveButton().disabled).toBe(true);
    expect(screen.queryByText(/Harness setting saved|changed elsewhere/)).toBeNull();

    // Saved feedback is equally bound to the visit that produced it.
    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    await userEvent.click(saveButton());
    expect(await screen.findByText("Harness setting saved.")).toBeTruthy();

    await userEvent.click(screen.getByRole("radio", { name: "Use Global Default" }));
    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    await userEvent.click(screen.getByRole("button", { name: /Project One revision 2/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
    expect(saveButton().disabled).toBe(true);
    expect(screen.queryByText("Harness setting saved.")).toBeNull();
  });

  it("drops an ordinary failure that settles while its Project is inactive", async () => {
    const pending = deferred<never>();
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
    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(setBinding).toHaveBeenCalledWith({
      projectId: "project-1",
      expectedRevision: 1n,
      selection: { case: "providerId", value: "fake" },
    });

    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    await act(async () => {
      await pending.reject(new ConnectError("offline", Code.Unavailable)).catch(() => undefined);
    });
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select DeepSeek Harness" }).checked,
    ).toBe(true);
    expect(
      screen.queryByText(
        "Provider catalog is temporarily unavailable. Global Default can still be selected.",
      ),
    ).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /Project One revision 1/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Use Global Default" }).checked,
    ).toBe(true);
    expect(
      screen.queryByText(
        "Provider catalog is temporarily unavailable. Global Default can still be selected.",
      ),
    ).toBeNull();
    expect(saveButton().disabled).toBe(true);
  });

  it("keeps concurrent saves on different Projects isolated when they complete in reverse order", async () => {
    const pendingOne = deferred<{ project: Project }>();
    const pendingTwo = deferred<{ project: Project }>();
    const setBinding = vi.fn((request: { projectId: string }) =>
      request.projectId === "project-1" ? pendingOne.promise : pendingTwo.promise,
    );
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
    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));

    await userEvent.click(screen.getByRole("button", { name: /Project Two revision 8/ }));
    await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
    await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
    expect(setBinding).toHaveBeenNthCalledWith(1, {
      projectId: "project-1",
      expectedRevision: 1n,
      selection: { case: "providerId", value: "fake" },
    });
    expect(setBinding).toHaveBeenNthCalledWith(2, {
      projectId: "project-2",
      expectedRevision: 8n,
      selection: { case: "providerId", value: "fake" },
    });

    // The later save settles first; the active editor only sees its own result.
    await act(async () => {
      await pendingTwo.resolve({ project: project("project-2", "Project Two", 9n, "fake") });
    });
    expect(await screen.findByText("Harness setting saved.")).toBeTruthy();
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);

    // Project One's earlier save settles while inactive: it may refresh its
    // own cache entry but must not clear Project Two's editor feedback.
    await act(async () => {
      await pendingOne.resolve({ project: project("project-1", "Project One", 2n, "fake") });
    });
    expect(screen.getByText("Harness setting saved.")).toBeTruthy();
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);

    await userEvent.click(screen.getByRole("button", { name: /Project One revision 2/ }));
    expect(
      screen.getByRole<HTMLInputElement>("radio", { name: "Select Fake Harness" }).checked,
    ).toBe(true);
    expect(screen.getAllByText("revision 2")).toHaveLength(2);
    expect(screen.queryByText("Harness setting saved.")).toBeNull();
  });

  it("ignores binding responses that settle after the Desktop unmounts", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const pending = deferred<{ project: Project }>();
      const first = render(
        <Desktop
          workosClients={clientFixture({
            projects: [project("project-1", "Project One", 1n)],
            setBinding: vi.fn(() => pending.promise),
          })}
        />,
      );
      await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
      await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
      await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
      first.unmount();
      await act(async () => {
        await pending.resolve({ project: project("project-1", "Project One", 2n, "fake") });
      });

      // The conflict-refresh continuation must be equally inert after
      // unmount: the save rejects, GetProject stays pending, then the tree
      // goes away before the refresh returns.
      const refresh = deferred<{ project: Project }>();
      const second = render(
        <Desktop
          workosClients={clientFixture({
            projects: [project("project-1", "Project One", 1n)],
            setBinding: vi.fn(() =>
              Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
            ),
            getProject: vi.fn(() => refresh.promise),
          })}
        />,
      );
      await userEvent.click(await screen.findByRole("button", { name: "Project settings" }));
      await userEvent.click(screen.getByRole("radio", { name: "Select Fake Harness" }));
      await userEvent.click(screen.getByRole("button", { name: "Save harness setting" }));
      await waitFor(() => {
        expect(refresh.promise).toBeTruthy();
      });
      second.unmount();
      await act(async () => {
        await refresh.resolve({ project: project("project-1", "Project One", 2n, "fake") });
      });
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("activates a newly created Project even when it is missing from the refreshed first page", async () => {
    const created = project("project-3", "Project Three", 1n);
    render(
      <Desktop
        workosClients={clientFixture({
          // The persisted page returned by listProjects stays capped at the
          // two original Projects, as it does once the workspace exceeds the
          // page size.
          projects: [
            project("project-1", "Project One", 1n),
            project("project-2", "Project Two", 8n, "deepseek"),
          ],
          createProject: vi.fn(() => Promise.resolve({ project: created })),
        })}
      />,
    );

    const nameInput = await screen.findByRole("textbox", { name: "Project name" });
    await userEvent.type(nameInput, "Project Three");
    await userEvent.click(screen.getByRole("button", { name: "Create space" }));

    const createdCard = await screen.findByRole("button", { name: /Project Three revision 1/ });
    expect(createdCard.className).toContain("active");
    expect((nameInput as HTMLInputElement).value).toBe("");
    expect(screen.queryByRole("alert")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Project settings" }));
    expect(screen.getByText("Harness provider")).toBeTruthy();
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
    // The refresh settled while Project One was inactive, so its conflict
    // feedback must not resurface; only the refreshed cache initializes the
    // editor.
    expect(screen.queryByText(/changed elsewhere/)).toBeNull();
  });
});

function surfaceSession(
  sessionId: string,
  installationId: string,
  projectId: string,
): SurfaceSession {
  return {
    $typeName: "workos.surface.v1.SurfaceSession",
    id: sessionId,
    appInstanceId: installationId,
    projectId,
    renderer: 1,
    url: `/surfaces/${sessionId}/`,
    bridgeToken: "",
    bridgeCapabilities: [],
    resize: false,
    clipboard: false,
    filePicker: false,
  };
}

function installedApp(appId: string, installationId: string, projectId: string) {
  return {
    $typeName: "workos.app.v1.AppInstallation",
    id: installationId,
    projectId,
    appId,
    version: "1.0.0",
    manifestDigest: "sha256:" + "a".repeat(64),
    installedAt: { seconds: 1787000000n, nanos: 0 },
    grantedPermissions: [],
    grantRevision: 1n,
  };
}

function clientFixture({
  projects,
  createProject = vi.fn(),
  setBinding = vi.fn(),
  getProject = vi.fn(),
  submitTask = vi.fn(),
  watchTaskEvents = vi.fn(() => eventStream([])),
  getTask = vi.fn(),
}: {
  projects: Project[];
  createProject?: ReturnType<typeof vi.fn>;
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
      createProject,
    },
    projectHarnessBindings: { setProjectHarnessBinding: setBinding },
    harnessCatalog: { getHarnessCatalog: vi.fn(() => Promise.resolve(providerCatalog())) },
    agentTasks: { submitTask, watchTaskEvents, getTask },
    appRegistry: {
      listApps: vi.fn(() => Promise.resolve({ apps: [], page: { nextPageToken: "" } })),
      getApp: vi.fn(() => Promise.resolve({ app: undefined })),
    },
    appInstallations: {
      listInstalledApps: vi.fn(() =>
        Promise.resolve({ installations: [], page: { nextPageToken: "" } }),
      ),
      installApp: vi.fn(),
      uninstallApp: vi.fn(),
      setAppGrants: vi.fn(),
    },
    artifacts: { createArtifact: vi.fn(), getArtifact: vi.fn(), listArtifacts: vi.fn() },
    surfaces: { createSurface: vi.fn(), closeSurface: vi.fn() },
  } as unknown as WorkOSClients;
}

async function* eventStream(events: AgentEvent[]) {
  await Promise.resolve();
  for (const event of events) yield { event };
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole<HTMLButtonElement>("button", { name: "Save harness setting" });
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
    reject: (reason?: unknown) => {
      reject(reason);
      return promise;
    },
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
      hardTokenBudget: false,
      hardRuntimeDeadline: false,
    },
  };
}

describe("app surface windows", () => {
  it("opens an installed app into a sandboxed surface window and closes its session", async () => {
    const user = userEvent.setup();
    const session = surfaceSession(
      "0198d7ea-2110-7c42-b659-c5e4d73bc341",
      "0198d7ea-2110-7c42-b659-c5e4d73bc342",
      "p1",
    );
    const catalogApp = {
      $typeName: "workos.app.v1.WorkOSApp",
      id: "notes-app",
      name: "Notes",
      version: "1.0.0",
      scope: 2,
      permissions: [],
      manifestDigest: "sha256:" + "a".repeat(64),
    };
    const clients = clientFixture({ projects: [project("p1", "Alpha", 1n)] });
    (clients.appRegistry.listApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({ apps: [catalogApp], page: { nextPageToken: "" } }),
    );
    (clients.appInstallations.listInstalledApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({
        installations: [installedApp("notes-app", session.appInstanceId, "p1")],
        page: { nextPageToken: "" },
      }),
    );
    (clients.surfaces.createSurface as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({ session }),
    );
    const closeSurface = vi.fn(() =>
      Promise.resolve({ $typeName: "workos.surface.v1.CloseSurfaceResponse" }),
    ) as unknown as typeof clients.surfaces.closeSurface;
    clients.surfaces.closeSurface = closeSurface;

    render(<Desktop workosClients={clients} />);
    await screen.findAllByText("Alpha");
    await user.click(screen.getByRole("button", { name: "App Library" }));
    await screen.findByText(/Installed · pinned 1\.0\.0/);
    await user.click(screen.getByRole("button", { name: "Open" }));

    const frame = await screen.findByTestId("app-surface-frame");
    expect(frame.getAttribute("sandbox")).toBe("allow-scripts");
    expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(frame.getAttribute("src")).toBe("/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/");
    // The agent center window is still rendered alongside the app window.
    expect(screen.getByLabelText("Agent goal")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Close App" }));
    await waitFor(() => {
      expect(closeSurface).toHaveBeenCalledWith({ surfaceSessionId: session.id });
    });
    expect(screen.queryByTestId("app-surface-frame")).toBeNull();
    expect(screen.getByLabelText("Agent goal")).toBeTruthy();
  });

  it("closes surface windows of other projects when the active project switches", async () => {
    const user = userEvent.setup();
    const session = surfaceSession(
      "0198d7ea-2110-7c42-b659-c5e4d73bc341",
      "0198d7ea-2110-7c42-b659-c5e4d73bc342",
      "p1",
    );
    const clients = clientFixture({
      projects: [project("p1", "Alpha", 1n), project("p2", "Beta", 1n)],
    });
    (clients.appRegistry.listApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({
        apps: [
          {
            $typeName: "workos.app.v1.WorkOSApp",
            id: "notes-app",
            name: "Notes",
            version: "1.0.0",
            scope: 2,
            permissions: [],
            manifestDigest: "sha256:" + "a".repeat(64),
          },
        ],
        page: { nextPageToken: "" },
      }),
    );
    (clients.appInstallations.listInstalledApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({
        installations: [installedApp("notes-app", session.appInstanceId, "p1")],
        page: { nextPageToken: "" },
      }),
    );
    (clients.surfaces.createSurface as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({ session }),
    );
    const closeSurface = vi.fn(() =>
      Promise.resolve({ $typeName: "workos.surface.v1.CloseSurfaceResponse" }),
    ) as unknown as typeof clients.surfaces.closeSurface;
    clients.surfaces.closeSurface = closeSurface;

    render(<Desktop workosClients={clients} />);
    await screen.findAllByText("Alpha");
    await user.click(screen.getByRole("button", { name: "App Library" }));
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByTestId("app-surface-frame")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: /Beta revision 1/ }));
    await waitFor(() => {
      expect(closeSurface).toHaveBeenCalledWith({ surfaceSessionId: session.id });
    });
    expect(screen.queryByTestId("app-surface-frame")).toBeNull();
  });

  // Leaving the Desktop entirely must not leave open sessions to age out via
  // TTL: every still-open app surface gets a best-effort idempotent Close.
  it("closes still-open app surfaces when the Desktop unmounts", async () => {
    const user = userEvent.setup();
    const session = surfaceSession(
      "0198d7ea-2110-7c42-b659-c5e4d73bc341",
      "0198d7ea-2110-7c42-b659-c5e4d73bc342",
      "p1",
    );
    const clients = clientFixture({ projects: [project("p1", "Alpha", 1n)] });
    (clients.appRegistry.listApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({
        apps: [
          {
            $typeName: "workos.app.v1.WorkOSApp",
            id: "notes-app",
            name: "Notes",
            version: "1.0.0",
            scope: 2,
            permissions: [],
            manifestDigest: "sha256:" + "a".repeat(64),
          },
        ],
        page: { nextPageToken: "" },
      }),
    );
    (clients.appInstallations.listInstalledApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({
        installations: [installedApp("notes-app", session.appInstanceId, "p1")],
        page: { nextPageToken: "" },
      }),
    );
    (clients.surfaces.createSurface as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({ session }),
    );
    const closeSurface = vi.fn(() =>
      Promise.resolve({ $typeName: "workos.surface.v1.CloseSurfaceResponse" }),
    ) as unknown as typeof clients.surfaces.closeSurface;
    clients.surfaces.closeSurface = closeSurface;

    const mounted = render(<Desktop workosClients={clients} />);
    await screen.findAllByText("Alpha");
    await user.click(screen.getByRole("button", { name: "App Library" }));
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByTestId("app-surface-frame")).toBeTruthy();

    mounted.unmount();
    await waitFor(() => {
      expect(closeSurface).toHaveBeenCalledWith({ surfaceSessionId: session.id });
    });
  });

  // A saved grant change must tear down exactly the managed installation's
  // surfaces: another installed app's window and session stay untouched.
  it("closes only the managed installation's surfaces after a grant change", async () => {
    const user = userEvent.setup();
    const boardSession = surfaceSession(
      "0198d7ea-2110-7c42-b659-c5e4d73bc341",
      "installation-board",
      "p1",
    );
    const notesSession = surfaceSession(
      "0198d7ea-2110-7c42-b659-c5e4d73bc399",
      "installation-notes",
      "p1",
    );
    const boardApp = {
      $typeName: "workos.app.v1.WorkOSApp",
      id: "board-app",
      name: "Board",
      version: "1.0.0",
      scope: 2,
      permissions: ["agent.task.run"],
      manifestDigest: "sha256:" + "a".repeat(64),
    };
    const notesApp = {
      $typeName: "workos.app.v1.WorkOSApp",
      id: "notes-app",
      name: "Notes",
      version: "1.0.0",
      scope: 2,
      permissions: [],
      manifestDigest: "sha256:" + "a".repeat(64),
    };
    const clients = clientFixture({ projects: [project("p1", "Alpha", 1n)] });
    (clients.appRegistry.listApps as ReturnType<typeof vi.fn>).mockReturnValue(
      Promise.resolve({ apps: [boardApp, notesApp], page: { nextPageToken: "" } }),
    );
    clients.appRegistry.getApp = vi.fn((request: { appId: string }) =>
      Promise.resolve({ app: request.appId === "board-app" ? boardApp : notesApp }),
    ) as unknown as typeof clients.appRegistry.getApp;
    (clients.appInstallations.listInstalledApps as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({
        installations: [
          {
            ...installedApp("board-app", "installation-board", "p1"),
            grantedPermissions: ["agent.task.run"],
          },
          installedApp("notes-app", "installation-notes", "p1"),
        ],
        page: { nextPageToken: "" },
      })
      // The dialog-opening fresh read still sees the granted epoch-1 row;
      // only the post-save reads see the revocation.
      .mockResolvedValueOnce({
        installations: [
          {
            ...installedApp("board-app", "installation-board", "p1"),
            grantedPermissions: ["agent.task.run"],
          },
          installedApp("notes-app", "installation-notes", "p1"),
        ],
        page: { nextPageToken: "" },
      })
      .mockResolvedValue({
        installations: [
          {
            ...installedApp("board-app", "installation-board", "p1"),
            grantedPermissions: [],
            grantRevision: 2n,
          },
          installedApp("notes-app", "installation-notes", "p1"),
        ],
        page: { nextPageToken: "" },
      });
    clients.surfaces.createSurface = vi.fn((request: { appInstanceId: string }) =>
      Promise.resolve({
        session: request.appInstanceId === "installation-board" ? boardSession : notesSession,
      }),
    ) as unknown as typeof clients.surfaces.createSurface;
    const closeSurface = vi.fn(() =>
      Promise.resolve({ $typeName: "workos.surface.v1.CloseSurfaceResponse" }),
    ) as unknown as typeof clients.surfaces.closeSurface;
    clients.surfaces.closeSurface = closeSurface;
    (clients.appInstallations.setAppGrants as ReturnType<typeof vi.fn>).mockResolvedValue({
      installation: {
        ...installedApp("board-app", "installation-board", "p1"),
        grantedPermissions: [],
        grantRevision: 2n,
      },
      projectRevision: 2n,
    });
    (clients.projects.getProject as ReturnType<typeof vi.fn>).mockResolvedValue({
      project: project("p1", "Alpha", 2n),
    });

    render(<Desktop workosClients={clients} />);
    await screen.findAllByText("Alpha");
    await user.click(screen.getByRole("button", { name: "App Library" }));
    // Both apps are installed, so both rows show the pinned version.
    await screen.findAllByText(/Installed · pinned 1\.0\.0/);
    // Open both apps: one surface window each. The rows are queried fresh
    // each time because the buttons re-render between the two opens.
    await user.click(screen.getAllByRole("button", { name: "Open" })[0] as HTMLElement);
    expect(await screen.findByTitle(`App surface ${boardSession.id}`)).toBeTruthy();
    await user.click(screen.getAllByRole("button", { name: "Open" })[1] as HTMLElement);
    expect(await screen.findByTitle(`App surface ${notesSession.id}`)).toBeTruthy();

    // Revoke board's only permission through the manage dialog.
    await user.click(
      screen.getAllByRole("button", { name: "Manage permissions" })[0] as HTMLElement,
    );
    expect(await screen.findByRole("dialog")).toBeTruthy();
    await user.click(screen.getByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));

    expect(
      await screen.findByText(
        "Permissions saved. Reopen the app for the new permissions to take effect.",
      ),
    ).toBeTruthy();
    // The managed installation's surface is closed best-effort; the other
    // app's window and session are untouched.
    await waitFor(() => {
      expect(closeSurface).toHaveBeenCalledWith({ surfaceSessionId: boardSession.id });
    });
    expect(closeSurface).not.toHaveBeenCalledWith({ surfaceSessionId: notesSession.id });
    expect(screen.queryByTitle(`App surface ${boardSession.id}`)).toBeNull();
    expect(screen.getByTitle(`App surface ${notesSession.id}`)).toBeTruthy();
    // The row reflects the server-confirmed replacement.
    expect(screen.getByText("Granted: none · grant revision 2")).toBeTruthy();
  });
});
