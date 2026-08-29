import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AppAgentApprovalDecision,
  AppAgentApprovalState,
  type AgentApproval,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";

interface Feedback {
  text: string;
  isError: boolean;
}

export function approvalStateLabel(state: AppAgentApprovalState): string {
  switch (state) {
    case AppAgentApprovalState.PENDING:
      return "pending";
    case AppAgentApprovalState.APPROVED:
      return "approved";
    case AppAgentApprovalState.REJECTED:
      return "rejected";
    case AppAgentApprovalState.EXPIRED:
      return "expired";
    default:
      return "unknown";
  }
}

export function decideErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The decision could not be saved.";
  switch (reason.code) {
    case Code.Aborted:
      return "This approval was already decided elsewhere.";
    case Code.FailedPrecondition:
      return "This approval can no longer be decided. Its policy, permissions, or provider changed.";
    case Code.ResourceExhausted:
      return "Approving would exceed the app's daily allowance. Raise the policy or wait for the UTC reset.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "The agent store is temporarily unavailable. Try again shortly.";
    default:
      return "The decision could not be saved.";
  }
}

// ApprovalsView is the owner-only pre-run approval surface: pending app tasks
// appear here before they ever queue, and a decision either reserves quota
// and enqueues the task (approve) or terminates it (reject). The list is
// scoped to the active project; goal excerpts are untrusted app content and
// render as text only.
export function ApprovalsView({
  workosClients,
  projectId,
  onDecided,
}: {
  workosClients: WorkOSClients;
  projectId: string;
  // onDecided lets the parent react after a successful decision.
  onDecided?: () => void;
}) {
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  const [approvals, setApprovals] = useState<AgentApproval[]>([]);
  const [feedback, setFeedback] = useState<Feedback>();
  const [busy, setBusy] = useState<Record<string, "APPROVE" | "REJECT" | undefined>>({});
  const generationRef = useRef(0);

  const refresh = useCallback(async () => {
    const generation = generationRef.current;
    setState("loading");
    try {
      const response = await workosClients.approvals.listApprovals({
        projectId,
        page: { pageSize: 100, pageToken: "" },
      });
      if (generationRef.current !== generation) return;
      setApprovals(response.approvals);
      setState("ready");
    } catch {
      if (generationRef.current !== generation) return;
      setState("error");
    }
  }, [workosClients, projectId]);

  useEffect(() => {
    // A late response from a previous project must not paint this one's list.
    generationRef.current += 1;
    void refresh();
    return () => {
      generationRef.current += 1;
    };
  }, [refresh]);

  async function decide(approvalId: string, decision: "APPROVE" | "REJECT") {
    if (busy[approvalId]) return;
    setBusy((current) => ({ ...current, [approvalId]: decision }));
    setFeedback(undefined);
    try {
      await workosClients.approvals.decideApproval({
        idempotencyKey: crypto.randomUUID(),
        approvalId,
        decision:
          decision === "APPROVE"
            ? AppAgentApprovalDecision.APPROVE
            : AppAgentApprovalDecision.REJECT,
      });
      await refresh();
      setFeedback({
        text:
          decision === "APPROVE"
            ? "Task approved and queued. The app's daily allowance now reserves this run."
            : "Task rejected. It will never execute and reserved no quota.",
        isError: false,
      });
      onDecided?.();
    } catch (reason) {
      await refresh();
      setFeedback({ text: decideErrorMessage(reason), isError: true });
    } finally {
      setBusy((current) => {
        const next = { ...current, [approvalId]: undefined };
        return next;
      });
    }
  }

  if (state === "loading") {
    return (
      <p className="approvals-state" role="status">
        Loading approvals…
      </p>
    );
  }
  if (state === "error") {
    return (
      <div className="approvals-state" role="alert">
        <p>Approvals are temporarily unavailable.</p>
        <Button
          onClick={() => {
            void refresh();
          }}
          type="button"
        >
          Retry approvals
        </Button>
      </div>
    );
  }
  const pending = approvals.filter((approval) => approval.state === AppAgentApprovalState.PENDING);
  const decided = approvals.filter((approval) => approval.state !== AppAgentApprovalState.PENDING);
  return (
    <div className="approvals-view">
      <h4>Pending approvals</h4>
      {feedback ? (
        <p
          className={feedback.isError ? "library-feedback error" : "library-feedback"}
          role={feedback.isError ? "alert" : "status"}
        >
          {feedback.text}
        </p>
      ) : null}
      {pending.length === 0 ? (
        <p className="empty-state">No tasks are waiting for your approval.</p>
      ) : (
        <ul className="approval-list" aria-label="Pending approvals">
          {pending.map((approval) => (
            <li className="approval-row" key={approval.approvalId}>
              <div className="approval-facts">
                <strong>Task {approval.taskId}</strong>
                <small>
                  app {approval.appId || approval.installationId} · provider {approval.providerId}
                </small>
                <small>
                  budget · {approval.maxOutputTokensPerTask.toString()} output tokens ·{" "}
                  {approval.maxRuntimeSecondsPerTask.toString()}s runtime · policy revision{" "}
                  {approval.policyRevision.toString()}
                </small>
                <p className="approval-goal">{approval.goalExcerpt}</p>
              </div>
              <div className="approval-row-actions">
                <Button
                  disabled={Boolean(busy[approval.approvalId])}
                  onClick={() => void decide(approval.approvalId, "APPROVE")}
                  type="button"
                >
                  {busy[approval.approvalId] === "APPROVE" ? "Approving…" : "Approve"}
                </Button>
                <Button
                  disabled={Boolean(busy[approval.approvalId])}
                  onClick={() => void decide(approval.approvalId, "REJECT")}
                  type="button"
                >
                  {busy[approval.approvalId] === "REJECT" ? "Rejecting…" : "Reject"}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      {decided.length > 0 ? (
        <>
          <h4>Recent decisions</h4>
          <ul className="approval-history" aria-label="Recent approval decisions">
            {decided.map((approval) => (
              <li key={approval.approvalId} data-state={approvalStateLabel(approval.state)}>
                Task {approval.taskId} · {approvalStateLabel(approval.state)}
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </div>
  );
}
