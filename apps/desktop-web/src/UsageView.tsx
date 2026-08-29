import { useCallback, useEffect, useRef, useState } from "react";
import type { WorkOSClients } from "@workos/agent-sdk";
import type { AgentAppDailyUsage } from "@workos/protocol";
import { Button } from "@workos/ui-kit";

// UsageView shows one (installation, UTC day) bucket per installed app.
// Reserved allowance and reported usage stay separate facts, and cost is only
// shown when a verified observation exists — never rendered as zero.
interface InstallationRow {
  id: string;
  appId: string;
}

export function UsageView({
  workosClients,
  projectId,
}: {
  workosClients: WorkOSClients;
  projectId: string;
}) {
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  const [rows, setRows] = useState<
    Array<{ installation: InstallationRow; usage: AgentAppDailyUsage }>
  >([]);
  const generationRef = useRef(0);

  const refresh = useCallback(async () => {
    const generation = generationRef.current;
    setState("loading");
    try {
      const date = new Date().toISOString().slice(0, 10);
      const installations: InstallationRow[] = [];
      let token = "";
      for (;;) {
        const page = await workosClients.appInstallations.listInstalledApps({
          projectId,
          page: { pageSize: 100, pageToken: token },
        });
        for (const installation of page.installations) {
          if (installation.uninstalledAt === undefined) {
            installations.push({ id: installation.id, appId: installation.appId });
          }
        }
        token = page.page?.nextPageToken ?? "";
        if (!token) break;
      }
      const loaded: Array<{ installation: InstallationRow; usage: AgentAppDailyUsage }> = [];
      for (const installation of installations) {
        const response = await workosClients.appUsage.getAppUsage({
          projectId,
          installationId: installation.id,
          utcDate: date,
        });
        if (!response.usage) throw new Error("missing usage response");
        loaded.push({ installation, usage: response.usage });
      }
      if (generationRef.current !== generation) return;
      setRows(loaded);
      setState("ready");
    } catch {
      if (generationRef.current !== generation) return;
      setState("error");
    }
  }, [workosClients, projectId]);

  useEffect(() => {
    generationRef.current += 1;
    void refresh();
    return () => {
      generationRef.current += 1;
    };
  }, [refresh]);

  if (state === "ready" && rows.length === 0) {
    return <p className="empty-state">Install an app to see its agent usage.</p>;
  }
  if (state === "loading") {
    return (
      <p className="approvals-state" role="status">
        Loading usage…
      </p>
    );
  }
  if (state === "error") {
    return (
      <div className="approvals-state" role="alert">
        <p>Usage is temporarily unavailable.</p>
        <Button
          onClick={() => {
            void refresh();
          }}
          type="button"
        >
          Retry usage
        </Button>
      </div>
    );
  }
  return (
    <div className="usage-view">
      <p className="usage-note">
        Buckets reset at midnight UTC. Reserved allowance is granted at enqueue time and is not
        refunded; reported usage is what providers actually observed.
      </p>
      <ul className="usage-list" aria-label="Daily agent usage">
        {rows.map(({ installation, usage }) => (
          <li className="usage-row" key={installation.id}>
            <strong>{installation.appId}</strong>
            <span data-testid="usage-date">{usage.utcDate}</span>
            <dl>
              <dt>Reserved</dt>
              <dd>
                {usage.tasksReserved.toString()} tasks · {usage.outputTokensReserved.toString()}{" "}
                output tokens
              </dd>
              <dt>Reported</dt>
              <dd>
                {usage.tasksRecorded.toString()} tasks · {usage.inputTokensRecorded.toString()} in /{" "}
                {usage.outputTokensRecorded.toString()} out
              </dd>
              <dt>Cost</dt>
              <dd>
                {usage.costDecimalRecorded === "" ? "unavailable" : usage.costDecimalRecorded}
              </dd>
              <dt>Circuit</dt>
              <dd>{usage.quotaBreached ? "breached — runs fail closed until reset" : "normal"}</dd>
            </dl>
          </li>
        ))}
      </ul>
    </div>
  );
}
