import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AppAgentExecutionMode,
  AppAgentPolicySource,
  type AppAgentPolicy,
  type AppInstallation,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";

export type PolicyDialogPhase = "loading" | "ready" | "load-error" | "saving" | "saved";

export interface PolicyDraft {
  mode: AppAgentExecutionMode;
  maxOutputTokensPerTask: string;
  maxRuntimeSecondsPerTask: string;
  maxTasksPerUtcDay: string;
  maxReservedOutputTokensPerUtcDay: string;
}

export function policySummaryLabel(policy: AppAgentPolicy): string {
  const mode = policy.spec?.executionMode ?? AppAgentExecutionMode.UNSPECIFIED;
  const modeLabel =
    mode === AppAgentExecutionMode.ALLOW
      ? "allow"
      : mode === AppAgentExecutionMode.REQUIRE_APPROVAL
        ? "require approval"
        : mode === AppAgentExecutionMode.BLOCK
          ? "block"
          : "unknown";
  return policy.source === AppAgentPolicySource.EXPLICIT
    ? `${modeLabel} · revision ${policy.policyRevision.toString()}`
    : `system default (${modeLabel})`;
}

function draftFromPolicy(policy: AppAgentPolicy): PolicyDraft {
  return {
    mode: policy.spec?.executionMode ?? AppAgentExecutionMode.UNSPECIFIED,
    maxOutputTokensPerTask: (policy.spec?.maxOutputTokensPerTask ?? 0).toString(),
    maxRuntimeSecondsPerTask: (policy.spec?.maxRuntimeSecondsPerTask ?? 0).toString(),
    maxTasksPerUtcDay: (policy.spec?.maxTasksPerUtcDay ?? 0).toString(),
    maxReservedOutputTokensPerUtcDay: (
      policy.spec?.maxReservedOutputTokensPerUtcDay ?? 0
    ).toString(),
  };
}

function parseLimit(value: string): number | undefined {
  if (!/^[1-9][0-9]*$/.test(value)) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : undefined;
}

export function draftIsValid(draft: PolicyDraft): boolean {
  return (
    (draft.mode === AppAgentExecutionMode.ALLOW ||
      draft.mode === AppAgentExecutionMode.REQUIRE_APPROVAL ||
      draft.mode === AppAgentExecutionMode.BLOCK) &&
    parseLimit(draft.maxOutputTokensPerTask) !== undefined &&
    parseLimit(draft.maxRuntimeSecondsPerTask) !== undefined &&
    parseLimit(draft.maxTasksPerUtcDay) !== undefined &&
    parseLimit(draft.maxReservedOutputTokensPerUtcDay) !== undefined
  );
}

export function saveErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The policy could not be saved.";
  switch (reason.code) {
    case Code.Aborted:
      return "The policy changed elsewhere. The latest revision was loaded.";
    case Code.InvalidArgument:
      return "Every limit must be a positive whole number inside its bound.";
    case Code.NotFound:
      return "This installation is no longer available.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "The agent store is temporarily unavailable. Try again shortly.";
    default:
      return "The policy could not be saved.";
  }
}

interface DialogFeedback {
  text: string;
  isError: boolean;
}

// PolicyDialog edits one installation's Agent execution policy as a full
// replacement (ADR-0005). It shows whether the effective policy is the
// versioned system default or an explicit row, submits an idempotent save
// with the loaded revision for optimistic concurrency, and reloads fresh
// facts on conflict instead of replaying stale edits.
export function PolicyDialog({
  installation,
  workosClients,
  onClose,
  onSaved,
}: {
  installation: AppInstallation;
  workosClients: Pick<WorkOSClients, "appPolicies">;
  onClose: () => void;
  onSaved?: (installationId: string, policy: AppAgentPolicy) => void;
}) {
  const [phase, setPhase] = useState<PolicyDialogPhase>("loading");
  const [loaded, setLoaded] = useState<AppAgentPolicy>();
  const [draft, setDraft] = useState<PolicyDraft>();
  const [feedback, setFeedback] = useState<DialogFeedback>();
  const generationRef = useRef(0);

  const load = useCallback(async () => {
    const generation = generationRef.current;
    setPhase("loading");
    try {
      const response = await workosClients.appPolicies.getAppPolicy({
        projectId: installation.projectId,
        installationId: installation.id,
      });
      if (generationRef.current !== generation || !response.policy) return;
      setLoaded(response.policy);
      setDraft(draftFromPolicy(response.policy));
      setPhase("ready");
    } catch (reason) {
      if (generationRef.current !== generation) return;
      if (reason instanceof ConnectError && reason.code === Code.NotFound) {
        setFeedback({ text: "This installation is no longer available.", isError: true });
        setPhase("load-error");
      } else {
        setFeedback({ text: "The agent policy is temporarily unavailable.", isError: true });
        setPhase("load-error");
      }
    }
  }, [installation.id, installation.projectId, workosClients]);

  useEffect(() => {
    generationRef.current += 1;
    void load();
    return () => {
      generationRef.current += 1;
    };
  }, [load]);

  async function save() {
    if (!draft || !loaded || phase === "saving" || !draftIsValid(draft)) return;
    setPhase("saving");
    setFeedback(undefined);
    const maxOutputTokens = parseLimit(draft.maxOutputTokensPerTask);
    const maxRuntimeSeconds = parseLimit(draft.maxRuntimeSecondsPerTask);
    const maxTasks = parseLimit(draft.maxTasksPerUtcDay);
    const maxReservedTokens = parseLimit(draft.maxReservedOutputTokensPerUtcDay);
    if (
      maxOutputTokens === undefined ||
      maxRuntimeSeconds === undefined ||
      maxTasks === undefined ||
      maxReservedTokens === undefined
    ) {
      return;
    }
    try {
      const response = await workosClients.appPolicies.setAppPolicy({
        idempotencyKey: crypto.randomUUID(),
        projectId: installation.projectId,
        installationId: installation.id,
        spec: {
          executionMode: draft.mode,
          maxOutputTokensPerTask: BigInt(maxOutputTokens),
          maxRuntimeSecondsPerTask: BigInt(maxRuntimeSeconds),
          maxTasksPerUtcDay: BigInt(maxTasks),
          maxReservedOutputTokensPerUtcDay: BigInt(maxReservedTokens),
        },
        expectedPolicyRevision:
          loaded.source === AppAgentPolicySource.EXPLICIT ? loaded.policyRevision : 0n,
      });
      if (!response.policy) throw new Error("missing policy response");
      onSaved?.(installation.id, response.policy);
      setLoaded(response.policy);
      setDraft(draftFromPolicy(response.policy));
      setPhase("saved");
      setFeedback({
        text: "Agent policy saved. It governs new app tasks; permissions are unchanged.",
        isError: false,
      });
    } catch (reason) {
      if (reason instanceof ConnectError && reason.code === Code.Aborted) {
        // The stored revision moved: reload fresh facts and make the user
        // re-confirm instead of replaying stale edits.
        await load();
        setFeedback({ text: saveErrorMessage(reason), isError: true });
        setPhase("ready");
      } else {
        setFeedback({ text: saveErrorMessage(reason), isError: true });
        setPhase("ready");
      }
    }
  }

  return (
    <div className="consent-backdrop" role="presentation">
      <div className="app-policy-dialog" role="dialog" aria-modal="true" aria-label="Agent policy">
        <header>
          <strong>Agent policy</strong>
          <small>
            {installation.appId} · pinned {installation.version}
          </small>
        </header>
        {phase === "loading" ? (
          <p className="library-state" role="status">
            Loading agent policy…
          </p>
        ) : null}
        {phase === "load-error" ? (
          <div className="library-state">
            <p role="alert">{feedback?.text}</p>
            <Button
              onClick={() => {
                void load();
              }}
              type="button"
            >
              Retry policy
            </Button>
          </div>
        ) : null}
        {draft && loaded ? (
          <>
            <p className="policy-note">
              The policy decides how new app tasks run and how much they may cost. It never replaces
              permissions — grants still bound every bridge call.
            </p>
            <fieldset disabled={phase === "saving"}>
              <legend>Execution mode</legend>
              {(
                [
                  [AppAgentExecutionMode.ALLOW, "Allow — run tasks immediately"],
                  [
                    AppAgentExecutionMode.REQUIRE_APPROVAL,
                    "Require approval — wait for my decision",
                  ],
                  [AppAgentExecutionMode.BLOCK, "Block — no new tasks"],
                ] as Array<[AppAgentExecutionMode, string]>
              ).map(([mode, label]) => (
                <label key={mode}>
                  <input
                    checked={draft.mode === mode}
                    name="execution-mode"
                    onChange={() => {
                      setDraft({ ...draft, mode });
                    }}
                    type="radio"
                    value={mode}
                  />{" "}
                  {label}
                </label>
              ))}
            </fieldset>
            <fieldset disabled={phase === "saving"}>
              <legend>Limits (UTC daily reset)</legend>
              <label>
                Output tokens per task
                <input
                  inputMode="numeric"
                  name="max-output-tokens"
                  onChange={(event) => {
                    setDraft({ ...draft, maxOutputTokensPerTask: event.target.value.trim() });
                  }}
                  value={draft.maxOutputTokensPerTask}
                />
              </label>
              <label>
                Runtime seconds per task
                <input
                  inputMode="numeric"
                  name="max-runtime-seconds"
                  onChange={(event) => {
                    setDraft({ ...draft, maxRuntimeSecondsPerTask: event.target.value.trim() });
                  }}
                  value={draft.maxRuntimeSecondsPerTask}
                />
              </label>
              <label>
                Tasks per UTC day
                <input
                  inputMode="numeric"
                  name="max-tasks"
                  onChange={(event) => {
                    setDraft({ ...draft, maxTasksPerUtcDay: event.target.value.trim() });
                  }}
                  value={draft.maxTasksPerUtcDay}
                />
              </label>
              <label>
                Reserved output tokens per UTC day
                <input
                  inputMode="numeric"
                  name="max-reserved-tokens"
                  onChange={(event) => {
                    setDraft({
                      ...draft,
                      maxReservedOutputTokensPerUtcDay: event.target.value.trim(),
                    });
                  }}
                  value={draft.maxReservedOutputTokensPerUtcDay}
                />
              </label>
            </fieldset>
            <p className="policy-facts">Current: {policySummaryLabel(loaded)}</p>
            {feedback ? (
              <p
                className={feedback.isError ? "library-feedback error" : "library-feedback"}
                role={feedback.isError ? "alert" : "status"}
              >
                {feedback.text}
              </p>
            ) : null}
            <div className="policy-dialog-actions">
              <Button onClick={onClose} type="button">
                Close
              </Button>
              <Button
                disabled={!draftIsValid(draft) || phase === "saving"}
                onClick={() => void save()}
                type="button"
              >
                {phase === "saving" ? "Saving…" : "Save policy"}
              </Button>
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}
