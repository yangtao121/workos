// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { AgentEvent } from "@workos/protocol";
import { AgentSessionsApp } from "./AgentSessions.js";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type RpcRequest = Record<string, unknown>;

// The desktop hosts the selected session so responsive remounts keep it.
function TestHost(props: { clients: WorkOSClients }) {
  const [selected, setSelected] = useState<string>();
  return (
    <AgentSessionsApp
      projectId="project-1"
      workosClients={props.clients}
      selectedSessionId={selected}
      onSelectSession={setSelected}
    />
  );
}

const session = {
  id: "session-1",
  projectId: "project-1",
  providerId: "fake",
  state: 1,
  activeTaskId: "",
};

function fixture() {
  const agentSessions = {
    listSessions: vi.fn<
      (request: RpcRequest) => Promise<{ sessions: unknown[]; nextPageToken: string }>
    >(() => Promise.resolve({ sessions: [{ ...session }], nextPageToken: "" })),
    listSessionInputs: vi.fn<(request: RpcRequest) => Promise<{ inputs: unknown[] }>>(() =>
      Promise.resolve({ inputs: [] }),
    ),
    createSession: vi.fn<(request: RpcRequest) => Promise<{ session: typeof session | undefined }>>(
      () => Promise.resolve({ session: { ...session } }),
    ),
    getSession: vi.fn<(request: RpcRequest) => Promise<{ session: unknown }>>(() =>
      Promise.resolve({ session: { ...session } }),
    ),
    submitSessionInput: vi.fn<(request: RpcRequest) => Promise<{ input?: unknown }>>(() =>
      Promise.resolve({}),
    ),
    getSessionInput: vi.fn<(request: RpcRequest) => Promise<{ input?: unknown }>>(() =>
      Promise.resolve({}),
    ),
    cancelSessionExecution: vi.fn<(request: RpcRequest) => Promise<Record<string, unknown>>>(() =>
      Promise.resolve({}),
    ),
    closeSession: vi.fn<(request: RpcRequest) => Promise<Record<string, unknown>>>(() =>
      Promise.resolve({}),
    ),
    watchSessionEvents: vi.fn(),
  };
  const agentTasks = {
    watchTaskEvents: vi.fn(() => asyncGenerator([])),
    getTask: vi.fn(() => Promise.resolve({})),
  };
  const projectWorkspaces = {
    listProjectWorkspaces: vi.fn<(request: RpcRequest) => Promise<{ bindings: unknown[] }>>(() =>
      Promise.resolve({
        bindings: [{ id: "binding-1", state: 1, displayName: "Fixture workspace", readOnly: true }],
      }),
    ),
  };
  return {
    agentSessions,
    agentTasks,
    projectWorkspaces,
    clients: { agentSessions, agentTasks, projectWorkspaces } as unknown as WorkOSClients,
  };
}

async function* asyncGenerator(events: AgentEvent[]): AsyncGenerator<{ event?: AgentEvent }> {
  for (const event of events) {
    yield await Promise.resolve({ event });
  }
}

function taskEvent(tag: string, partial: Record<string, unknown>): AgentEvent {
  return { id: `event-${tag}`, sequence: BigInt(tag), ...partial } as AgentEvent;
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("Agent session window", () => {
  it("lists sessions with first-input names and provider facts", async () => {
    const f = fixture();
    f.agentSessions.listSessionInputs = vi.fn(() =>
      Promise.resolve({
        inputs: [
          { id: "input-1", sessionId: "session-1", text: "Draft the launch plan", state: 3 },
        ],
      }),
    );
    render(<TestHost clients={f.clients} />);
    expect(await screen.findByText("Draft the launch plan")).toBeTruthy();
    expect(screen.getByText("fake")).toBeTruthy();
    expect(f.projectWorkspaces.listProjectWorkspaces).toHaveBeenCalledWith({
      projectId: "project-1",
      includeArchived: false,
    });
  });

  it("creates a session with a stable idempotency key and opens it", async () => {
    const f = fixture();
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("new-agent-session"));
    await waitFor(() => {
      expect(f.agentSessions.createSession).toHaveBeenCalledWith({
        projectId: "project-1",
        idempotencyKey: expect.stringMatching(/^.{8,}$/) as string,
      });
    });
    expect(await screen.findByTestId("agent-session-view")).toBeTruthy();
    expect(screen.getByTestId("agent-session-provider").textContent).toBe("Provider · fake");
    expect(await screen.findByText("Workspace · Fixture workspace")).toBeTruthy();
  });

  it("submits an input with one client id and renders the deterministic run", async () => {
    const f = fixture();
    const dispatched = {
      id: "input-1",
      sessionId: "session-1",
      clientInputId: "",
      text: "prove the session run",
      state: 2,
      taskId: "task-1",
      sequence: 1n,
      resultSummary: "",
    };
    f.agentSessions.getSession = vi.fn(() =>
      Promise.resolve({ session: { ...session, activeTaskId: "task-1" } }),
    );
    // The input starts DISPATCHED and flips to COMPLETED once the task's
    // event stream reaches its terminal event (the stream replay finishes).
    let runState = 2;
    f.agentSessions.listSessionInputs = vi.fn(() =>
      Promise.resolve({
        inputs: [{ ...dispatched, state: runState, resultSummary: runState === 3 ? "done" : "" }],
      }),
    );
    f.agentSessions.submitSessionInput = vi.fn(() => Promise.resolve({ input: { ...dispatched } }));
    f.agentTasks.watchTaskEvents = vi.fn(() =>
      (async function* () {
        yield {
          event: taskEvent("1", {
            event: { case: "runStarted", value: { runId: "run-1", providerId: "fake" } },
          }),
        };
        yield {
          event: taskEvent("2", {
            event: { case: "assistantMessage", value: { text: "prove the session run" } },
          }),
        };
        yield {
          event: taskEvent("3", {
            event: {
              case: "runCompleted",
              value: { summary: "Task task-1 completed by fake harness" },
            },
          }),
        };
        // The server finalizes the input once the run reached its terminal
        // event; the terminal refresh that follows observes COMPLETED.
        await Promise.resolve();
        runState = 3;
      })(),
    );
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("new-agent-session"));
    await userEvent.type(await screen.findByLabelText("Session message"), "prove the session run");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => {
      expect(f.agentSessions.submitSessionInput).toHaveBeenCalledWith({
        sessionId: "session-1",
        clientInputId: expect.stringMatching(/^.{8,}$/) as string,
        text: "prove the session run",
      });
    });
    expect(
      await screen.findByText("prove the session run", { selector: ".agent-timeline p" }),
    ).toBeTruthy();
    expect(screen.getByText("Run started · fake")).toBeTruthy();
    expect(screen.getByText("Task task-1 completed by fake harness")).toBeTruthy();
    // The finished conversation keeps its replayed timeline: the terminal
    // refresh must not blank the messages.
    await waitFor(() => {
      expect(
        screen
          .getByTestId("agent-session-transcript")
          .querySelector(".session-input")
          ?.getAttribute("data-input-state"),
      ).toBe("done");
    });
    expect(screen.getByText("Run started · fake")).toBeTruthy();
    expect(
      screen.getByText("prove the session run", { selector: ".agent-timeline p" }),
    ).toBeTruthy();
  });

  it("shows the queued state, queue hint, and the stop-versus-close actions", async () => {
    const f = fixture();
    f.agentSessions.getSession = vi.fn(() =>
      Promise.resolve({ session: { ...session, activeTaskId: "task-1" } }),
    );
    f.agentSessions.listSessionInputs = vi.fn(() =>
      Promise.resolve({
        inputs: [
          {
            id: "input-1",
            sessionId: "session-1",
            text: "first",
            state: 2,
            taskId: "task-1",
            sequence: 1n,
          },
          {
            id: "input-2",
            sessionId: "session-1",
            text: "second",
            state: 1,
            taskId: "",
            sequence: 2n,
          },
        ],
      }),
    );
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("agent-session-entry-session-1"));
    expect((await screen.findByTestId("agent-session-queue-hint")).textContent).toBe(
      "1 queued input will run in order.",
    );
    const queued = screen.getByText("second").closest("li");
    expect(queued?.getAttribute("data-input-state")).toBe("queued");
    await userEvent.click(screen.getByTestId("agent-session-cancel"));
    await waitFor(() => {
      expect(f.agentSessions.cancelSessionExecution).toHaveBeenCalledWith({
        sessionId: "session-1",
        reason: "Stopped from the session window",
      });
    });
    expect(f.agentSessions.closeSession).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId("agent-session-close"));
    await waitFor(() => {
      expect(f.agentSessions.closeSession).toHaveBeenCalledWith({ sessionId: "session-1" });
    });
  });

  it("recovers a timed-out submission through GetSessionInput and never resubmits", async () => {
    const f = fixture();
    let resolveSubmit!: (value: unknown) => void;
    f.agentSessions.submitSessionInput = vi.fn<
      (request: RpcRequest) => Promise<{ input?: unknown }>
    >(
      () =>
        new Promise((resolve) => {
          resolveSubmit = resolve as (value: unknown) => void;
        }),
    );
    f.agentSessions.getSessionInput = vi.fn(() =>
      Promise.resolve({
        input: {
          id: "input-9",
          sessionId: "session-1",
          clientInputId: "client-9",
          text: "slow submit",
          state: 1,
          taskId: "",
          sequence: 1n,
        },
      }),
    );
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("agent-session-entry-session-1"));
    await userEvent.type(await screen.findByLabelText("Session message"), "slow submit");
    vi.useFakeTimers();
    await act(async () => {
      screen.getByRole("button", { name: "Send" }).click();
      await vi.advanceTimersByTimeAsync(13_000);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    vi.useRealTimers();
    // The timeout verdict moves the pending input to recovery; the recovery
    // poll re-reads by the exact client id — never a second submission.
    await waitFor(() => {
      expect(f.agentSessions.getSessionInput).toHaveBeenCalledWith({
        sessionId: "session-1",
        clientInputId: expect.stringMatching(/^.{8,}$/) as string,
      });
    });
    expect(f.agentSessions.submitSessionInput).toHaveBeenCalledTimes(1);
    resolveSubmit(undefined);
  });

  it("disables the composer once the session is closed", async () => {
    const f = fixture();
    f.agentSessions.getSession = vi.fn(() =>
      Promise.resolve({ session: { ...session, state: 3 } }),
    );
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("agent-session-entry-session-1"));
    const composer = await screen.findByLabelText("Session message");
    expect((composer as HTMLTextAreaElement).disabled).toBe(true);
    expect(screen.queryByTestId("agent-session-close")).toBeNull();
  });

  it("keeps the create key stable across a failed creation retry", async () => {
    const f = fixture();
    f.agentSessions.createSession
      .mockRejectedValueOnce(new Error("gateway hiccup"))
      .mockResolvedValueOnce({ session: { ...session } });
    render(<TestHost clients={f.clients} />);
    await userEvent.click(await screen.findByTestId("new-agent-session"));
    expect(await screen.findByRole("alert")).toBeTruthy();
    await userEvent.click(screen.getByTestId("new-agent-session"));
    await waitFor(() => {
      expect(f.agentSessions.createSession).toHaveBeenCalledTimes(2);
    });
    const first = f.agentSessions.createSession.mock.calls[0]?.[0] as { idempotencyKey: string };
    const second = f.agentSessions.createSession.mock.calls[1]?.[0] as { idempotencyKey: string };
    expect(second.idempotencyKey).toBe(first.idempotencyKey);
    expect(await screen.findByTestId("agent-session-view")).toBeTruthy();
  });

  it("surfaces an honest empty verdict when the service lists nothing", async () => {
    const f = fixture();
    f.agentSessions.listSessions = vi.fn(() =>
      Promise.resolve({ sessions: [], nextPageToken: "" }),
    );
    render(<TestHost clients={f.clients} />);
    expect(await screen.findByText("No open sessions in this project yet.")).toBeTruthy();
  });
});
