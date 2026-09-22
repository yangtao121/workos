import { create } from "@bufbuild/protobuf";
import { useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AgentSessionState,
  SessionDirectiveKind,
  SessionDirectiveSchema,
  type AgentSession,
  type HarnessCapabilities,
  type SessionDirective,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import { ArtifactViewerWindow } from "./ArtifactViewerWindow.js";

export function SessionAutomation({
  session,
  capabilities,
  busy,
  controlling,
  submit,
  pause,
  workosClients,
}: {
  session: AgentSession;
  capabilities: HarnessCapabilities | undefined;
  busy: boolean;
  controlling: boolean;
  submit: (directive: SessionDirective) => void;
  pause: () => void;
  workosClients: WorkOSClients;
}) {
  const [reviewId, setReviewId] = useState("");
  const goal = session.goal;
  const open = session.state === AgentSessionState.ACTIVE;
  const supported = capabilities?.sessionGoals === true;
  const createGoal = supported && open && !busy && (!goal || goal.phase === "complete");
  const children = session.delegations;
  const command = (kind: SessionDirectiveKind) => {
    if (!goal) return;
    submit(
      create(SessionDirectiveSchema, { kind, goalRef: goal.ref, expectedRevision: goal.revision }),
    );
  };
  return (
    <>
      {goal ? (
        <section
          className="session-automation"
          aria-label="Session goal"
          data-testid="session-goal"
        >
          <div className="session-automation-heading">
            <strong>Goal</strong>
            <span className="session-state-chip">
              {goal.pauseRequested
                ? "pause requested"
                : goal.phase === "active" && !goal.armed
                  ? "waiting for resume"
                  : goal.phase}
            </span>
          </div>
          <p>{goal.objective}</p>
          <p className="session-automation-detail">
            {goal.roundsStarted} / {goal.maxRounds} automatic rounds
          </p>
          {goal.blockedReason ? <p role="status">{goal.blockedReason}</p> : null}
          {supported && open ? (
            <div className="session-automation-actions">
              {goal.phase === "active" ? (
                <Button
                  disabled={controlling || goal.pauseRequested}
                  onClick={() => {
                    if (session.activeTaskId) pause();
                    else command(SessionDirectiveKind.PAUSE_GOAL);
                  }}
                  type="button"
                >
                  Pause goal
                </Button>
              ) : null}
              {goal.phase !== "complete" &&
              !busy &&
              !goal.armed &&
              goal.roundsStarted < goal.maxRounds ? (
                <Button
                  disabled={controlling}
                  onClick={() => {
                    command(SessionDirectiveKind.RESUME_GOAL);
                  }}
                  type="button"
                >
                  Resume goal
                </Button>
              ) : null}
            </div>
          ) : null}
        </section>
      ) : null}
      {createGoal ? (
        <details className="session-automation">
          <summary>Run toward a goal</summary>
          <form
            className="session-goal-form"
            onSubmit={(event) => {
              event.preventDefault();
              const form = new FormData(event.currentTarget);
              const rawObjective = form.get("objective");
              const objective = typeof rawObjective === "string" ? rawObjective.trim() : "";
              if (!objective) return;
              submit(
                create(SessionDirectiveSchema, {
                  kind: SessionDirectiveKind.CREATE_GOAL,
                  objective,
                  maxRounds: Number(form.get("rounds")),
                }),
              );
            }}
          >
            <label>
              Goal objective
              <textarea required maxLength={4096} name="objective" />
            </label>
            <label>
              Maximum rounds
              <input name="rounds" type="number" min={1} max={32} defaultValue={8} required />
            </label>
            <p className="session-automation-detail">
              Continues within this execution’s time and token budget. You can pause between steps.
            </p>
            <Button disabled={controlling} type="submit">
              Start goal
            </Button>
          </form>
        </details>
      ) : null}
      {children.length ? (
        <section
          className="session-automation"
          aria-label="Delegated tasks"
          data-testid="session-delegations"
        >
          <strong>Delegated tasks</strong>
          <p className="session-automation-detail">
            Each task works in its own copy. Review changes before applying them to the project.
          </p>
          <ul className="session-delegation-list">
            {children.map((child) => (
              <li key={child.id}>
                <div className="session-automation-heading">
                  <strong>{child.title}</strong>
                  <span className="session-state-chip">{child.state.replaceAll("_", " ")}</span>
                </div>
                {child.resultSummary ? <p>{child.resultSummary}</p> : null}
                {child.baseCommit ? (
                  <p className="session-automation-detail">
                    Base commit {child.baseCommit.slice(0, 8)}
                  </p>
                ) : null}
                {child.resultArtifactId ? (
                  <Button
                    type="button"
                    onClick={() => {
                      setReviewId(child.resultArtifactId);
                    }}
                  >
                    Review changes
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
          {reviewId ? (
            <div className="session-delegation-review">
              <Button
                type="button"
                onClick={() => {
                  setReviewId("");
                }}
              >
                Close review
              </Button>
              <ArtifactViewerWindow
                artifactId={reviewId}
                projectId={session.projectId}
                workosClients={workosClients}
              />
            </div>
          ) : null}
        </section>
      ) : null}
    </>
  );
}
