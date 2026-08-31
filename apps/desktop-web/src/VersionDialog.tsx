import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { AppInstallation, AppInstallationVersionSnapshot, Project } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import type { WorkOSClients } from "@workos/agent-sdk";

// VersionDialog is the owner's explicit version surface for one installed
// app (ADR-0012): it lists the installation's durable version history and
// offers exactly two owner-triggered commands — pinning one explicit
// registry version (transition) and restoring the most recent previous
// pinned version (rollback). Both are Core commands with an expected
// project revision and a fresh idempotency key; the dialog never submits a
// digest, image, or any other server-derived fact. A command whose target
// requested permissions do not cover the current grants fails closed with
// the permissions-need-review copy — permissions are never expanded here.
export function VersionDialog({
  project,
  installation,
  workosClients,
  onFactsRefreshed,
  onVersionChanged,
  onCancel,
}: {
  project: Project;
  installation: AppInstallation;
  workosClients: Pick<WorkOSClients, "appInstallations" | "projects">;
  onFactsRefreshed: (project: Project, installations: AppInstallation[]) => void;
  onVersionChanged: (installationId: string) => void;
  onCancel: () => void;
}) {
  const [history, setHistory] = useState<AppInstallationVersionSnapshot[]>();
  const [historyUnavailable, setHistoryUnavailable] = useState(false);
  const [target, setTarget] = useState("");
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<{ text: string; isError: boolean }>();
  const generationRef = useRef(0);

  useEffect(() => {
    const generation = ++generationRef.current;
    void workosClients.appInstallations
      .listAppVersionHistory({
        projectId: project.id,
        installationId: installation.id,
        page: { pageSize: 20 },
      })
      .then((response) => {
        if (generation !== generationRef.current) return;
        setHistory(response.snapshots);
      })
      .catch(() => {
        if (generation !== generationRef.current) return;
        setHistoryUnavailable(true);
      });
    return () => {
      generationRef.current += 1;
    };
  }, [installation.id, project.id, workosClients.appInstallations]);

  const refreshFacts = useCallback(async () => {
    const [projectResponse, active] = await Promise.all([
      workosClients.projects.getProject({ projectId: project.id }),
      listInstallations(workosClients, project.id),
    ]);
    const refreshed = projectResponse.project;
    if (!refreshed) throw new Error("missing refreshed project");
    onFactsRefreshed(refreshed, active);
    return active;
  }, [installation.id, onFactsRefreshed, project.id, workosClients]);

  const runCommand = useCallback(
    (command: (revision: bigint) => Promise<void>, successText: string) => {
      if (busy) return;
      setBusy(true);
      setFeedback(undefined);
      const generation = generationRef.current;
      const isLive = () => generation === generationRef.current;
      void (async () => {
        try {
          await command(project.revision);
          if (!isLive()) return;
          try {
            const active = await refreshFacts();
            if (!isLive()) return;
            const updated = active.find((item) => item.id === installation.id);
            setFeedback({
              text: updated ? `${successText} The app now pins ${updated.version}.` : successText,
              isError: false,
            });
            onVersionChanged(installation.id);
          } catch {
            if (isLive()) {
              setFeedback({
                text: `${successText} The library could not be refreshed.`,
                isError: false,
              });
              onVersionChanged(installation.id);
            }
          }
        } catch (reason) {
          if (!isLive()) return;
          setFeedback({ text: commandErrorMessage(reason), isError: true });
          if (reason instanceof ConnectError && reason.code === Code.Aborted) {
            try {
              await refreshFacts();
            } catch {
              // The conflict copy stands on its own; a failed refresh is not
              // a second error.
            }
          }
        } finally {
          if (isLive()) setBusy(false);
        }
      })();
    },
    [
      busy,
      installation.id,
      onFactsRefreshed,
      onVersionChanged,
      project.id,
      project.revision,
      refreshFacts,
    ],
  );

  const transition = (version: string) => {
    const trimmed = version.trim();
    if (!trimmed) return;
    runCommand(
      (revision) =>
        workosClients.appInstallations
          .transitionAppVersion({
            idempotencyKey: crypto.randomUUID(),
            projectId: project.id,
            installationId: installation.id,
            expectedProjectRevision: revision,
            version: trimmed,
          })
          .then(() => undefined),
      `Switched to ${trimmed}.`,
    );
  };

  const previous = rollbackTarget(history, installation.version);
  const rollback = () => {
    if (!previous) return;
    runCommand(
      (revision) =>
        workosClients.appInstallations
          .rollbackAppVersion({
            idempotencyKey: crypto.randomUUID(),
            projectId: project.id,
            installationId: installation.id,
            expectedProjectRevision: revision,
          })
          .then(() => undefined),
      `Rolled back to ${previous}.`,
    );
  };

  return (
    <div className="consent-backdrop" role="presentation">
      <div
        aria-describedby="version-dialog-description"
        aria-labelledby="version-dialog-title"
        aria-modal="true"
        className="app-consent version-dialog"
        role="dialog"
      >
        <h3 id="version-dialog-title">
          Versions · {installation.appId} · pinned {installation.version}
        </h3>
        <p id="version-dialog-description">
          Switching versions keeps this installation and its permissions. A target whose permissions
          do not cover the current grants is rejected — review permissions first. Version facts live
          on the server; this dialog never submits digests or images.
        </p>
        {historyUnavailable ? (
          <p className="app-consent-note" role="alert">
            The version history is temporarily unavailable.
          </p>
        ) : history === undefined ? (
          <p className="app-consent-note">Loading version history…</p>
        ) : (
          <ul className="version-history" aria-label="Version history">
            {history.map((snapshot) => (
              <li
                className={
                  snapshot.version === installation.version ? "version-row current" : "version-row"
                }
                key={String(snapshot.sequence)}
              >
                <strong>{snapshot.version}</strong>
                <span>
                  {snapshot.source}
                  {snapshot.version === installation.version ? " · pinned" : ""}
                </span>
              </li>
            ))}
          </ul>
        )}
        {feedback ? (
          <p
            className={feedback.isError ? "library-feedback error" : "library-feedback"}
            role={feedback.isError ? "alert" : "status"}
          >
            {feedback.text}
          </p>
        ) : null}
        <div className="app-consent-actions">
          <input
            aria-label="Target version"
            name="target-version"
            onChange={(event) => {
              setTarget(event.target.value);
            }}
            placeholder="e.g. 1.2.0"
            value={target}
          />
          <Button
            disabled={busy || !target.trim()}
            onClick={() => {
              transition(target);
            }}
            type="button"
          >
            {busy ? "Working…" : "Switch version"}
          </Button>
          <Button disabled={busy || !previous} onClick={rollback} type="button">
            {previous ? `Roll back to ${previous}` : "No previous version"}
          </Button>
          <Button disabled={busy} onClick={onCancel} type="button">
            Close
          </Button>
        </div>
      </div>
    </div>
  );
}

// rollbackTarget computes the dialog's preview of the previous pinned
// version: the newest history snapshot whose version differs from the
// current pin. The server re-derives the actual rollback target from the
// durable history and decides authoritatively.
function rollbackTarget(
  history: AppInstallationVersionSnapshot[] | undefined,
  currentVersion: string,
): string | undefined {
  if (!history || history.length === 0) return undefined;
  for (let index = history.length - 1; index >= 0; index -= 1) {
    const snapshot = history[index];
    if (snapshot && snapshot.version !== currentVersion) return snapshot.version;
  }
  return undefined;
}

function commandErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The version change could not be saved.";
  switch (reason.code) {
    case Code.FailedPrecondition:
      if (reason.message.includes("permissions")) {
        return "Permissions need review: the target version does not cover the current grants. Open Manage permissions and re-confirm.";
      }
      return "There is no previous version to roll back to.";
    case Code.NotFound:
      return "The app, project, or installation is no longer available.";
    case Code.Aborted:
      return "The project changed elsewhere. The latest revision was loaded — retry.";
    case Code.AlreadyExists:
      return "A different version of this app is already installed.";
    default:
      return "The version change could not be saved.";
  }
}

async function listInstallations(
  workosClients: Pick<WorkOSClients, "appInstallations">,
  projectId: string,
): Promise<AppInstallation[]> {
  const installations: AppInstallation[] = [];
  let token = "";
  for (;;) {
    const page = await workosClients.appInstallations.listInstalledApps({
      projectId,
      page: { pageSize: 100, pageToken: token },
    });
    installations.push(...page.installations);
    if (page.page?.nextPageToken === "") break;
    token = page.page?.nextPageToken ?? "";
  }
  return installations;
}
