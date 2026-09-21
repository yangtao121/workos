// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AgentSessionSchema,
  AgentSessionState,
  HarnessCapabilitiesSchema,
  SessionDirectiveKind,
  SessionGoalSchema,
} from "@workos/protocol";
import { SessionAutomation } from "./SessionAutomation.js";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(cleanup);
function fixture() {
  return {
    session: create(AgentSessionSchema, {
      id: "session",
      projectId: "project",
      state: AgentSessionState.ACTIVE,
    }),
    capabilities: create(HarnessCapabilitiesSchema, { sessionGoals: true }),
    busy: false,
    controlling: false,
    submit: vi.fn(),
    pause: vi.fn(),
    workosClients: {} as WorkOSClients,
  };
}
it("submits a bounded explicit goal and hides controls without provider support", async () => {
  const f = fixture();
  const view = render(<SessionAutomation {...f} />);
  await userEvent.click(screen.getByText("Run toward a goal"));
  await userEvent.type(screen.getByLabelText("Goal objective"), "Test the release");
  await userEvent.click(screen.getByRole("button", { name: "Start goal" }));
  expect(f.submit).toHaveBeenCalledWith(
    expect.objectContaining({
      kind: SessionDirectiveKind.CREATE_GOAL,
      objective: "Test the release",
      maxRounds: 8,
    }),
  );
  view.rerender(<SessionAutomation {...f} capabilities={undefined} />);
  expect(screen.queryByText("Run toward a goal")).toBeNull();
});
it("requests a running goal pause immediately and uses the current revision when resuming", async () => {
  const f = fixture();
  f.session.activeTaskId = "task";
  f.busy = true;
  f.session.goal = create(AgentSessionSchema, {
    goal: {
      ref: "native-goal",
      revision: 9n,
      objective: "Finish tests",
      phase: "active",
      armed: true,
      roundsStarted: 1,
      maxRounds: 4,
    },
  }).goal;
  const view = render(<SessionAutomation {...f} />);
  await userEvent.click(screen.getByRole("button", { name: "Pause goal" }));
  expect(f.pause).toHaveBeenCalledOnce();
  expect(f.submit).not.toHaveBeenCalled();
  f.session = create(AgentSessionSchema, {
    id: f.session.id,
    projectId: f.session.projectId,
    state: f.session.state,
    activeTaskId: "",
    goal: create(SessionGoalSchema, {
      ref: "native-goal",
      objective: "Finish tests",
      roundsStarted: 1,
      maxRounds: 4,
      phase: "paused",
      armed: false,
      revision: 10n,
    }),
  });
  f.busy = false;
  view.rerender(<SessionAutomation {...f} />);
  await userEvent.click(screen.getByRole("button", { name: "Resume goal" }));
  expect(f.submit).toHaveBeenCalledWith(
    expect.objectContaining({
      kind: SessionDirectiveKind.RESUME_GOAL,
      goalRef: "native-goal",
      expectedRevision: 10n,
    }),
  );
});
it("does not offer to resume an exhausted goal or conceal an interrupted child", () => {
  const f = fixture();
  f.session = create(AgentSessionSchema, {
    id: f.session.id,
    projectId: f.session.projectId,
    state: f.session.state,
    goal: {
      ref: "goal",
      phase: "blocked",
      roundsStarted: 4,
      maxRounds: 4,
      blockedReason: "Round limit reached",
    },
    delegations: [
      {
        id: "child",
        title: "Check output",
        state: "needs_review",
        resultSummary: "Execution was interrupted; review its worktree.",
      },
    ],
  });
  render(<SessionAutomation {...f} />);
  expect(screen.queryByRole("button", { name: "Resume goal" })).toBeNull();
  expect(screen.getByText("Round limit reached")).toBeTruthy();
  expect(screen.getByText("needs review")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
});
