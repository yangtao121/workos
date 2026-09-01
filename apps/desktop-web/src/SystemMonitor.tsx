import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@workos/ui-kit";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  IncidentRestartOutcome,
  IncidentSeverity,
  IncidentState,
  IncidentViolation,
  type Incident,
} from "@workos/protocol";

// System Monitor is the minimal, non-permanent reliability window: it lists
// the current project's incidents with their severity, state, violation,
// restart outcome, and acknowledgement fact, and offers two owner-scoped
// actions — acknowledge, and (ADR-0012) rolling an app installation back to
// its previous pinned version when the installation's durable history has
// one. It never shows host endpoints, container or cgroup IDs, raw logs, or
// engine output — the service contract is summarized facts only (ADR-0006).
// When the reliability upstream is unreachable the window degrades to a
// fixed notice and nothing else on the desktop is affected.
export function SystemMonitor({
  projectId,
  expectedProjectRevision,
  workosClients,
  onProjectRevisionChanged,
  onInstallationVersionChanged,
}: {
  projectId: string | undefined;
  // The active project's server revision, used as the rollback command's
  // optimistic-concurrency etag. A conflict reloads authoritative state.
  expectedProjectRevision: bigint | undefined;
  workosClients: WorkOSClients;
  onProjectRevisionChanged?: (revision: bigint) => void;
  onInstallationVersionChanged?: (installationId: string) => void;
}) {
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [state, setState] = useState<"loading" | "ready" | "unavailable" | "empty-project">(
    "loading",
  );
  const [acknowledging, setAcknowledging] = useState<string | undefined>(undefined);
  const [acknowledged, setAcknowledged] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState<string | undefined>(undefined);
  // Per-installation rollback facts: the previewed previous version while a
  // command is not running, and a bounded feedback per incident row.
  // undefined means not loaded; null is the authoritative "no previous
  // version" verdict. Keeping those states distinct prevents an empty
  // history from triggering an unbounded request/render loop.
  const [rollbackTargets, setRollbackTargets] = useState<Record<string, string | null>>({});
  const [rollbackUnavailable, setRollbackUnavailable] = useState<ReadonlySet<string>>(new Set());
  const [rollbackBusy, setRollbackBusy] = useState<string | undefined>(undefined);
  const [rollbackNotice, setRollbackNotice] = useState<string | undefined>(undefined);
  const rollbackLoadingRef = useRef(new Set<string>());
  const generationRef = useRef(0);

  const load = useCallback(() => {
    const generation = ++generationRef.current;
    const isLive = () => generation === generationRef.current;
    if (!projectId) {
      if (isLive()) {
        setIncidents([]);
        setRollbackTargets({});
        setRollbackUnavailable(new Set());
        rollbackLoadingRef.current.clear();
        setState("empty-project");
      }
      return;
    }
    setIncidents([]);
    setRollbackTargets({});
    setRollbackUnavailable(new Set());
    rollbackLoadingRef.current.clear();
    setState("loading");
    void workosClients.incidents
      .listIncidents({ projectId, page: { pageSize: 20 } })
      .then((response) => {
        if (!isLive()) return;
        setIncidents(response.incidents);
        setState("ready");
      })
      .catch((reason: unknown) => {
        if (!isLive()) return;
        setState("unavailable");
        if (reason instanceof ConnectError && reason.code !== Code.Unavailable) {
          setNotice("The reliability view is not available right now.");
        }
      });
  }, [projectId, workosClients]);

  useEffect(() => {
    load();
  }, [load]);

  // Resolve rollback eligibility lazily for every actionable incident bound
  // to an app instance of this project. Eligibility is a preview only: the
  // rollback command re-derives its target from the durable history and
  // fails closed when there is none.
  useEffect(() => {
    if (state !== "ready" || !projectId) return;
    const generation = generationRef.current;
    const isLive = () => generation === generationRef.current;
    const candidates = new Set(
      incidents
        .filter(
          (incident) =>
            Boolean(incident.appInstanceId) &&
            incident.state !== IncidentState.RESOLVED &&
            !rollbackUnavailable.has(incident.appInstanceId) &&
            !rollbackLoadingRef.current.has(incident.appInstanceId) &&
            rollbackTargets[incident.appInstanceId] === undefined,
        )
        .map((incident) => incident.appInstanceId),
    );
    for (const instanceId of candidates) {
      rollbackLoadingRef.current.add(instanceId);
      void workosClients.appInstallations
        .listAppVersionHistory({
          projectId,
          installationId: instanceId,
          page: { pageSize: 20 },
        })
        .then((response) => {
          if (!isLive()) return;
          const snapshots = response.snapshots;
          const current = snapshots[snapshots.length - 1]?.version;
          const previous = findPrevious(snapshots, current);
          rollbackLoadingRef.current.delete(instanceId);
          setRollbackTargets((record) => ({ ...record, [instanceId]: previous ?? null }));
        })
        .catch(() => {
          if (!isLive()) return;
          rollbackLoadingRef.current.delete(instanceId);
          setRollbackUnavailable((current) => new Set(current).add(instanceId));
        });
    }
  }, [
    incidents,
    projectId,
    rollbackTargets,
    rollbackUnavailable,
    state,
    workosClients.appInstallations,
  ]);

  function acknowledge(incidentId: string) {
    if (acknowledging) return;
    setAcknowledging(incidentId);
    setNotice(undefined);
    const generation = generationRef.current;
    void workosClients.incidents
      .acknowledgeIncident({
        incidentId,
        // A fresh key per click: acknowledge is one-way and idempotent, so a
        // double click replays the same decision instead of failing.
        idempotencyKey: crypto.randomUUID(),
      })
      .then(() => {
        if (generation !== generationRef.current) return;
        setAcknowledged((current) => {
          const next = new Set(current);
          next.add(incidentId);
          return next;
        });
      })
      .catch(() => {
        if (generation !== generationRef.current) return;
        setNotice("The acknowledgement could not be saved.");
      })
      .finally(() => {
        if (generation === generationRef.current) setAcknowledging(undefined);
      });
  }

  function rollbackToPrevious(incident: Incident) {
    if (rollbackBusy || !projectId || expectedProjectRevision === undefined) return;
    setRollbackBusy(incident.id);
    setRollbackNotice(undefined);
    const generation = generationRef.current;
    void workosClients.appInstallations
      .rollbackAppVersion({
        idempotencyKey: crypto.randomUUID(),
        projectId,
        installationId: incident.appInstanceId,
        expectedProjectRevision: expectedProjectRevision,
      })
      .then((response) => {
        if (generation !== generationRef.current) return;
        onProjectRevisionChanged?.(response.projectRevision);
        onInstallationVersionChanged?.(incident.appInstanceId);
        setRollbackTargets((current) => withoutRollbackTarget(current, incident.appInstanceId));
        setRollbackNotice(
          `Core restored ${response.rolledBackToVersion} for this app. It takes effect the next time the app runs — Core completing the switch is not the same as the app reporting healthy.`,
        );
      })
      .catch(async (reason: unknown) => {
        if (generation !== generationRef.current) return;
        if (reason instanceof ConnectError && reason.code === Code.Aborted) {
          try {
            const response = await workosClients.projects.getProject({ projectId });
            if (generation === generationRef.current && response.project) {
              onProjectRevisionChanged?.(response.project.revision);
            }
          } catch {
            // The conflict verdict is still authoritative. A failed refresh
            // never retries the mutation or hides the conflict.
          }
          if (generation !== generationRef.current) return;
          setRollbackTargets((current) => withoutRollbackTarget(current, incident.appInstanceId));
        }
        setRollbackNotice(rollbackErrorMessage(reason));
      })
      .finally(() => {
        if (generation === generationRef.current) setRollbackBusy(undefined);
      });
  }

  if (state === "empty-project") {
    return <p className="empty-state">Create a project to monitor its workloads.</p>;
  }
  if (state === "loading") {
    return <p className="empty-state">Loading incidents…</p>;
  }
  if (state === "unavailable") {
    return (
      <div className="system-monitor-body">
        <p className="empty-state">
          The reliability view is temporarily unavailable. Workload limits and supervision keep
          running independently of this window.
        </p>
        <Button onClick={load} type="button">
          Retry
        </Button>
      </div>
    );
  }
  return (
    <div className="system-monitor-body">
      {notice ? (
        <p className="error-toast" role="alert">
          {notice}
        </p>
      ) : null}
      {rollbackNotice ? (
        <p className="library-feedback" role="status">
          {rollbackNotice}
        </p>
      ) : null}
      {incidents.length === 0 ? (
        <p className="empty-state">No incidents recorded for this project.</p>
      ) : (
        <ul className="incident-list" aria-label="Project incidents">
          {incidents.map((incident) => {
            const previous = rollbackTargets[incident.appInstanceId];
            const rollbackEligible =
              incident.appInstanceId !== "" &&
              incident.state !== IncidentState.RESOLVED &&
              typeof previous === "string";
            return (
              <li className="incident-row" key={incident.id}>
                <div className="incident-facts">
                  <strong>{incident.summary}</strong>
                  <span>
                    {severityLabel(incident.severity)} · {stateLabel(incident.state)} ·{" "}
                    {violationLabel(incident.violation)} · {outcomeLabel(incident.restartOutcome)}
                  </span>
                </div>
                <span className="incident-actions">
                  {rollbackEligible ? (
                    <Button
                      disabled={rollbackBusy !== undefined || expectedProjectRevision === undefined}
                      onClick={() => {
                        rollbackToPrevious(incident);
                      }}
                      type="button"
                    >
                      {rollbackBusy === incident.id ? "Working…" : `Roll back to ${previous}`}
                    </Button>
                  ) : null}
                  {incident.acknowledgedAt || acknowledged.has(incident.id) ? (
                    <span className="incident-acknowledged">Acknowledged</span>
                  ) : (
                    <Button
                      disabled={acknowledging !== undefined}
                      onClick={() => {
                        acknowledge(incident.id);
                      }}
                      type="button"
                    >
                      {acknowledging === incident.id ? "Saving…" : "Acknowledge"}
                    </Button>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function withoutRollbackTarget(
  targets: Readonly<Record<string, string | null>>,
  appInstanceId: string,
): Record<string, string | null> {
  return Object.fromEntries(Object.entries(targets).filter(([key]) => key !== appInstanceId));
}

// findPrevious previews the rollback target: the newest snapshot whose
// version differs from the installation's current (last) version. The
// rollback command re-derives this from the durable history server-side.
function findPrevious(
  snapshots: Array<{ version: string }>,
  current: string | undefined,
): string | undefined {
  if (current === undefined) return undefined;
  for (let index = snapshots.length - 1; index >= 0; index -= 1) {
    const snapshot = snapshots[index];
    if (snapshot && snapshot.version !== current) return snapshot.version;
  }
  return undefined;
}

function rollbackErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The rollback could not be started.";
  switch (reason.code) {
    case Code.FailedPrecondition:
      if (reason.message.includes("permissions")) {
        return "Permissions need review: the previous version does not cover the current grants. Confirm them in the App Library first.";
      }
      return "There is no previous version to roll back to.";
    case Code.Aborted:
      return "The project changed elsewhere. The latest revision was loaded — retry.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "Core is temporarily unreachable. The rollback was not started.";
    default:
      return "The rollback could not be started.";
  }
}

function severityLabel(severity: IncidentSeverity): string {
  switch (severity) {
    case IncidentSeverity.INFO:
      return "Info";
    case IncidentSeverity.WARNING:
      return "Warning";
    case IncidentSeverity.CRITICAL:
      return "Critical";
    default:
      return "Severity unknown";
  }
}

function stateLabel(state: IncidentState): string {
  switch (state) {
    case IncidentState.OPEN:
      return "Open";
    case IncidentState.MITIGATED:
      return "Mitigated";
    case IncidentState.RESOLVED:
      return "Resolved";
    default:
      return "Unknown state";
  }
}

function violationLabel(violation: IncidentViolation): string {
  switch (violation) {
    case IncidentViolation.UNEXPECTED_EXIT:
      return "Unexpected exit";
    case IncidentViolation.HEALTH_FAILURE:
      return "Health failure";
    case IncidentViolation.OOM:
      return "Memory limit";
    case IncidentViolation.PIDS_LIMIT:
      return "Process limit";
    case IncidentViolation.RESTART_LIMIT_EXHAUSTED:
      return "Restart budget spent";
    default:
      return "Fault";
  }
}

function outcomeLabel(outcome: IncidentRestartOutcome): string {
  switch (outcome) {
    case IncidentRestartOutcome.PENDING:
      return "Action pending";
    case IncidentRestartOutcome.RESTARTED:
      return "Restarted";
    case IncidentRestartOutcome.STOPPED:
      return "Stopped";
    case IncidentRestartOutcome.FAILED:
      return "Action failed";
    default:
      return "No action yet";
  }
}
