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
// restart outcome, and acknowledgement fact, and offers one owner-scoped
// acknowledge action. It never shows host endpoints, container or cgroup
// IDs, raw logs, or engine output — the service contract is summarized facts
// only (ADR-0006). When the reliability upstream is unreachable the window
// degrades to a fixed notice and nothing else on the desktop is affected.
export function SystemMonitor({
  projectId,
  workosClients,
}: {
  projectId: string | undefined;
  workosClients: WorkOSClients;
}) {
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [state, setState] = useState<"loading" | "ready" | "unavailable" | "empty-project">(
    "loading",
  );
  const [acknowledging, setAcknowledging] = useState<string | undefined>(undefined);
  const [acknowledged, setAcknowledged] = useState<ReadonlySet<string>>(new Set());
  const [notice, setNotice] = useState<string | undefined>(undefined);
  const generationRef = useRef(0);

  const load = useCallback(() => {
    const generation = ++generationRef.current;
    const isLive = () => generation === generationRef.current;
    if (!projectId) {
      if (isLive()) setState("empty-project");
      return;
    }
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
      {incidents.length === 0 ? (
        <p className="empty-state">No incidents recorded for this project.</p>
      ) : (
        <ul className="incident-list" aria-label="Project incidents">
          {incidents.map((incident) => (
            <li className="incident-row" key={incident.id}>
              <div className="incident-facts">
                <strong>{incident.summary}</strong>
                <span>
                  {severityLabel(incident.severity)} · {stateLabel(incident.state)} ·{" "}
                  {violationLabel(incident.violation)} · {outcomeLabel(incident.restartOutcome)}
                </span>
              </div>
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
            </li>
          ))}
        </ul>
      )}
    </div>
  );
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
