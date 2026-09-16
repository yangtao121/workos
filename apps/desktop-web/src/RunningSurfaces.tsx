import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import { SurfaceRenderer, type SurfaceWorkloadView } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

// Running apps (B08): the honest, server-derived list of the project's live
// session workloads. Rows show only facts ListProjectSurfaces reports —
// display name, state, attachment count — with open/stop actions. Open
// re-attaches the same workload (never a second program); Stop is the
// explicit stop that a window close deliberately is not (ADR-0031).
export function RunningSurfaces(props: {
  projectId?: string | undefined;
  workosClients: WorkOSClients;
  onOpenTerminal: () => void;
  onOpenNative: () => void;
  onOpenAppInstance: (appInstanceId: string) => void;
}) {
  const { projectId, workosClients, onOpenTerminal, onOpenNative, onOpenAppInstance } = props;
  const [workloads, setWorkloads] = useState<SurfaceWorkloadView[]>([]);
  const [state, setState] = useState<"loading" | "ready" | "unavailable">("loading");
  const [error, setError] = useState("");
  const actions = useRef(new Map<string, string>());
  const [stopping, setStopping] = useState<string[]>([]);
  const generationRef = useRef(0);
  const reloadRef = useRef<() => void>(() => undefined);

  useEffect(() => {
    if (!projectId) {
      setWorkloads([]);
      setState("loading");
      return;
    }
    const generation = ++generationRef.current;
    let cancelled = false;
    // Read through a closure so narrowing cannot constant-fold the flag
    // while the cleanup closure can still flip it.
    const isCancelled = () => cancelled;
    const load = async () => {
      try {
        const response = await workosClients.surfaceContinuity.listProjectSurfaces({ projectId });
        if (isCancelled() || generation !== generationRef.current) return;
        setWorkloads(response.workloads);
        setState("ready");
      } catch {
        if (isCancelled() || generation !== generationRef.current) return;
        setWorkloads([]);
        setState("unavailable");
      }
    };
    reloadRef.current = () => void load();
    reloadRef.current();
    const timer = window.setInterval(reloadRef.current, 10_000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
      generationRef.current += 1;
    };
  }, [projectId, workosClients]);

  const stop = useCallback(
    async (workloadId: string, restart = false) => {
      if (stopping.includes(workloadId)) return;
      const generation = generationRef.current;
      setStopping((current) => [...current, workloadId]);
      const intent = `${restart ? "restart" : "stop"}:${workloadId}`;
      const actionKey = actions.current.get(intent) ?? crypto.randomUUID();
      actions.current.set(intent, actionKey);
      setError("");
      try {
        const request = { workloadId, actionKey };
        if (restart) await workosClients.surfaceContinuity.restartSurfaceWorkload(request);
        else await workosClients.surfaceContinuity.stopSurfaceWorkload(request);
        actions.current.delete(intent);
      } catch {
        if (generation === generationRef.current)
          setError("The action was not confirmed. Retry to check the same request.");
      } finally {
        if (generation === generationRef.current) {
          setStopping((current) => current.filter((id) => id !== workloadId));
          reloadRef.current();
        }
      }
    },
    [stopping, workosClients],
  );

  const openWorkload = useCallback(
    (workload: SurfaceWorkloadView) => {
      if (workload.appInstanceId) {
        onOpenAppInstance(workload.appInstanceId);
        return;
      }
      if (workload.renderer === SurfaceRenderer.REMOTE_NATIVE) onOpenNative();
      else onOpenTerminal();
    },
    [onOpenAppInstance, onOpenNative, onOpenTerminal],
  );

  return (
    <section className="running-apps" data-testid="running-apps" aria-label="Running apps">
      <header className="running-apps-heading">
        <h2>Running apps</h2>
        <span>Live session workloads in this project.</span>
      </header>
      {state === "unavailable" ? (
        <p className="running-apps-note" role="status">
          Live app sessions are unavailable in this deployment.
        </p>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
      <ul className="running-apps-list">
        {workloads.map((workload) => (
          <li
            className="running-app-entry"
            data-state={workload.state}
            data-workload-id={workload.workloadId}
            key={workload.workloadId}
          >
            <span className="running-app-name">{workload.displayName || "Session workload"}</span>
            <span className="running-app-facts">
              <span className="session-state-chip" data-state={workload.state}>
                {workload.state}
              </span>
              <span>{`${String(workload.attachmentCount)} attached`}</span>
            </span>
            <span className="running-app-actions">
              <Button
                disabled={workload.state !== "running"}
                onClick={() => {
                  openWorkload(workload);
                }}
                type="button"
              >
                Open
              </Button>
              {workload.state === "running" ? (
                <Button
                  className="running-app-stop"
                  disabled={stopping.includes(workload.workloadId)}
                  onClick={() => void stop(workload.workloadId)}
                  type="button"
                >
                  {stopping.includes(workload.workloadId) ? "Stopping…" : "Stop"}
                </Button>
              ) : null}
              <Button
                disabled={stopping.includes(workload.workloadId)}
                onClick={() => void stop(workload.workloadId, true)}
                type="button"
              >
                Restart
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {state === "ready" && workloads.length === 0 ? (
        <p className="running-apps-note">No live app sessions in this project.</p>
      ) : null}
    </section>
  );
}
