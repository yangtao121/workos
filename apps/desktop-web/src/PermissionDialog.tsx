import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import type { AppInstallation, Project, WorkOSApp } from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import type { WorkOSClients } from "@workos/agent-sdk";

/** Server-owned facts a grant flow needs: fresh project revision + installations. */
export interface PermissionFacts {
  project: Project;
  installations: AppInstallation[];
}

interface PermissionDialogProps {
  project: Project;
  installation: AppInstallation;
  workosClients: Pick<WorkOSClients, "appRegistry" | "appInstallations">;
  readFacts: () => Promise<PermissionFacts>;
  onFactsRefreshed: (facts: PermissionFacts) => void;
  onGrantsApplied: (installationId: string) => void;
  onCancel: () => void;
}

type DialogPhase =
  // The exact pinned registry version is still resolving.
  | "loading"
  // Checkboxes are editable (or a save is in flight, which reuses the view).
  | "ready"
  // The exact version could not be loaded; only a retry is offered.
  | "load-error"
  // The resolved app does not match the installation's pinned facts: fail
  // closed, no submittable checkboxes at all.
  | "mismatch"
  // A revision conflict was detected but the reload of fresh facts failed.
  | "reload-error"
  // The installation disappeared (e.g. uninstalled elsewhere).
  | "gone"
  // SetAppGrants is in flight.
  | "saving"
  // The server confirmed the replacement.
  | "saved";

interface VerifiedVersion {
  app: WorkOSApp;
  requestedPermissions: string[];
}

interface EditableGrant {
  appId: string;
  version: string;
  grantRevision: bigint;
  currentGrant: string[];
}

// Grants are stored canonically sorted, so the dialog submits the sorted
// selection and the server validates the rest (same rule as install).
function sortedCapabilities(values: readonly string[]): string[] {
  return [...values].sort((left, right) => left.localeCompare(right));
}

function sameSortedSet(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

// The registry response must describe exactly the installed version: app id,
// version, and manifest digest all have to match the installation's pinned
// facts. Any drift is fail closed — the requested set of another version must
// never become the basis for editing this installation's grant.
function matchesInstallation(app: WorkOSApp, installation: AppInstallation): boolean {
  return (
    app.id === installation.appId &&
    app.version === installation.version &&
    app.manifestDigest === installation.manifestDigest
  );
}

function editableFrom(installation: AppInstallation): EditableGrant {
  return {
    appId: installation.appId,
    version: installation.version,
    grantRevision: installation.grantRevision,
    currentGrant: [...installation.grantedPermissions],
  };
}

// PermissionDialog edits one installation's grant as a full replacement. It
// resolves the exact pinned registry version (never the catalog's current
// version), verifies the returned identity against the installation, seeds
// the checkboxes with the current grant — never with everything — and sends
// SetAppGrants only on an explicit Save with a fresh idempotency key and the
// UI's current project revision. A revision conflict reloads the fresh facts
// and requires the user to reconfirm; the previous selection is never
// replayed. Every in-flight promise is generation-guarded: closing the
// dialog, switching projects (the library remounts), or unmounting makes
// late results inert, and a late success can never close another app's
// windows.
export function PermissionDialog({
  project,
  installation,
  workosClients,
  readFacts,
  onFactsRefreshed,
  onGrantsApplied,
  onCancel,
}: PermissionDialogProps) {
  const [phase, setPhase] = useState<DialogPhase>("loading");
  const [verified, setVerified] = useState<VerifiedVersion>();
  const [grant, setGrant] = useState<EditableGrant>();
  const [selected, setSelected] = useState<string[]>([]);
  const [notice, setNotice] = useState<string>();
  const [saveError, setSaveError] = useState<string>();
  // Generation guards every in-flight promise of this dialog instance:
  // unmount (close, project switch, library teardown) invalidates late
  // responses so they cannot touch state or fire callbacks.
  const generationRef = useRef(0);
  useEffect(() => {
    return () => {
      generationRef.current += 1;
    };
  }, []);

  const adoptVersion = useCallback((app: WorkOSApp, factsSource: AppInstallation) => {
    setVerified({ app, requestedPermissions: sortedCapabilities(app.permissions) });
    setGrant(editableFrom(factsSource));
    // Checkboxes start from the current grant, never from a default-all
    // selection: an unchanged set must be a visible no-op.
    setSelected(sortedCapabilities(factsSource.grantedPermissions));
  }, []);

  const loadExactVersion = useCallback(async () => {
    const generation = generationRef.current;
    const isLive = () => generation === generationRef.current;
    setPhase("loading");
    setSaveError(undefined);
    setNotice(undefined);
    try {
      // Always the installation's exact pinned version: the catalog's
      // current version may request a different permission set.
      const response = await workosClients.appRegistry.getApp({
        appId: installation.appId,
        version: installation.version,
      });
      if (!isLive()) return;
      const app = response.app;
      if (!app || !matchesInstallation(app, installation)) {
        setPhase("mismatch");
        return;
      }
      adoptVersion(app, installation);
      setPhase("ready");
    } catch {
      if (!isLive()) return;
      setPhase("load-error");
    }
  }, [adoptVersion, installation, workosClients]);

  useEffect(() => {
    void loadExactVersion();
  }, [loadExactVersion]);

  // After a revision conflict the dialog reloads the server-owned facts —
  // project, installation, and the exact pinned version — resets the
  // checkboxes to the fresh current grant, and asks the user to confirm
  // again. The stale selection is discarded, never replayed.
  const reloadAfterConflict = useCallback(
    async (isLive: () => boolean) => {
      try {
        const facts = await readFacts();
        if (!isLive()) return;
        const fresh = facts.installations.find((item) => item.id === installation.id);
        if (!fresh) {
          onFactsRefreshed(facts);
          setPhase("gone");
          return;
        }
        const response = await workosClients.appRegistry.getApp({
          appId: fresh.appId,
          version: fresh.version,
        });
        if (!isLive()) return;
        onFactsRefreshed(facts);
        const app = response.app;
        if (!app || !matchesInstallation(app, fresh)) {
          setPhase("mismatch");
          return;
        }
        adoptVersion(app, fresh);
        setNotice(
          "Project settings changed elsewhere. Review the latest permissions and save again.",
        );
        setPhase("ready");
      } catch {
        if (!isLive()) return;
        setPhase("reload-error");
      }
    },
    [adoptVersion, installation.id, onFactsRefreshed, readFacts, workosClients],
  );

  const retryConflictReload = useCallback(() => {
    const generation = generationRef.current;
    setPhase("loading");
    void reloadAfterConflict(() => generation === generationRef.current);
  }, [reloadAfterConflict]);

  const save = useCallback(async () => {
    if (phase !== "ready" || !grant) return;
    const canonicalSelection = sortedCapabilities(selected);
    if (sameSortedSet(canonicalSelection, sortedCapabilities(grant.currentGrant))) return;
    const generation = generationRef.current;
    const isLive = () => generation === generationRef.current;
    setPhase("saving");
    setSaveError(undefined);
    setNotice(undefined);
    try {
      const response = await workosClients.appInstallations.setAppGrants({
        // Fresh key per Save: each explicit confirmation is its own command.
        idempotencyKey: crypto.randomUUID(),
        projectId: project.id,
        installationId: installation.id,
        expectedProjectRevision: project.revision,
        grantedPermissions: canonicalSelection,
      });
      if (!isLive()) return;
      // The server confirmed the replacement: every open surface of exactly
      // this installation is stale now, so the host tears its windows down
      // best-effort before anything else.
      onGrantsApplied(installation.id);
      const applied = response.installation;
      let nextGrant: EditableGrant | undefined;
      try {
        const facts = await readFacts();
        if (!isLive()) return;
        onFactsRefreshed(facts);
        const fresh = facts.installations.find((item) => item.id === installation.id);
        if (fresh) nextGrant = editableFrom(fresh);
      } catch {
        // The re-read is best-effort; the Set response below already carries
        // the authoritative saved grant.
      }
      if (!isLive()) return;
      setGrant(nextGrant ?? (applied ? editableFrom(applied) : grant));
      setPhase("saved");
    } catch (reason) {
      if (!isLive()) return;
      if (reason instanceof ConnectError && reason.code === Code.Aborted) {
        await reloadAfterConflict(isLive);
      } else {
        setSaveError(saveErrorMessage(reason));
        setPhase("ready");
      }
    }
  }, [
    grant,
    installation.id,
    onFactsRefreshed,
    onGrantsApplied,
    phase,
    project.id,
    project.revision,
    readFacts,
    reloadAfterConflict,
    selected,
    workosClients,
  ]);

  const requested = verified?.requestedPermissions ?? [];
  const currentGrant = grant?.currentGrant ?? [];
  const selectedSet = new Set(selected);
  const added = selected.filter((permission) => !currentGrant.includes(permission));
  const removed = currentGrant.filter((permission) => !selected.includes(permission));
  const hasChanges = added.length > 0 || removed.length > 0;
  const revokeAll = selected.length === 0 && currentGrant.length > 0;
  const busy = phase === "saving";
  const editable = (phase === "ready" || phase === "saving") && verified !== undefined;

  return (
    <div className="consent-backdrop" role="presentation">
      <div
        aria-describedby="app-permissions-description"
        aria-labelledby="app-permissions-title"
        aria-modal="true"
        className="app-consent app-permissions"
        role="dialog"
      >
        <h3 id="app-permissions-title">
          Manage permissions{" "}
          {verified
            ? `${verified.app.name || verified.app.id} ${verified.app.version}`
            : installation.appId}
        </h3>

        {phase === "loading" ? (
          <p className="app-consent-note" role="status">
            Loading app permissions…
          </p>
        ) : null}

        {phase === "load-error" ? (
          <div className="app-consent-note" role="alert">
            <p>The pinned app version could not be loaded.</p>
            <Button onClick={() => void loadExactVersion()} type="button">
              Retry permissions
            </Button>
          </div>
        ) : null}

        {phase === "mismatch" ? (
          <p className="app-consent-note" role="alert">
            The app version could not be verified. Permissions cannot be edited.
          </p>
        ) : null}

        {phase === "reload-error" ? (
          <div className="app-consent-note" role="alert">
            <p>Project settings changed elsewhere and could not be refreshed.</p>
            <Button onClick={retryConflictReload} type="button">
              Retry refresh
            </Button>
          </div>
        ) : null}

        {phase === "gone" ? (
          <p className="app-consent-note" role="alert">
            The app is no longer installed.
          </p>
        ) : null}

        {editable && grant ? (
          <>
            <p id="app-permissions-description">
              Requested permissions for pinned version {grant.version}. Saving replaces the whole
              set. Current grant revision {grant.grantRevision.toString()}.
            </p>
            {notice ? (
              <p className="app-permissions-notice" role="status">
                {notice}
              </p>
            ) : null}
            {saveError ? (
              <p className="app-permissions-error" role="alert">
                {saveError}
              </p>
            ) : null}
            {requested.length === 0 ? (
              <p className="app-consent-note">This app requests no permissions.</p>
            ) : (
              <ul className="app-consent-list">
                {requested.map((permission) => (
                  <li key={permission}>
                    <label>
                      <input
                        aria-label={permission}
                        checked={selectedSet.has(permission)}
                        disabled={busy}
                        onChange={(event) => {
                          setSelected((current) =>
                            event.target.checked
                              ? current.concat(permission)
                              : current.filter((item) => item !== permission),
                          );
                        }}
                        type="checkbox"
                      />
                      <span>{permission}</span>
                    </label>
                  </li>
                ))}
              </ul>
            )}
            {revokeAll ? (
              <p className="app-permissions-notice">
                Saving with nothing selected revokes every permission.
              </p>
            ) : null}
            {hasChanges ? (
              <ul aria-label="Permission changes" className="app-permissions-diff">
                {added.map((permission) => (
                  <li className="permission-added" key={`add-${permission}`}>
                    Adding {permission}
                  </li>
                ))}
                {removed.map((permission) => (
                  <li className="permission-removed" key={`remove-${permission}`}>
                    Removing {permission}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="app-permissions-diff-empty">No changes to save.</p>
            )}
            <div className="app-consent-actions">
              <Button disabled={!hasChanges || busy} onClick={() => void save()} type="button">
                {busy ? "Saving…" : revokeAll ? "Revoke all permissions" : "Save permissions"}
              </Button>
              <Button disabled={busy} onClick={onCancel} type="button">
                Cancel
              </Button>
            </div>
          </>
        ) : null}

        {phase === "saved" ? (
          <>
            <p role="status">
              Permissions saved. Reopen the app for the new permissions to take effect.
            </p>
            <div className="app-consent-actions">
              <Button onClick={onCancel} type="button">
                Close
              </Button>
            </div>
          </>
        ) : null}
      </div>
    </div>
  );
}

function saveErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The permission change could not be saved.";
  switch (reason.code) {
    case Code.NotFound:
      return "The app or project is no longer available.";
    case Code.InvalidArgument:
    case Code.PermissionDenied:
      return "The permission change was rejected. Reload and try again.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "The permission change could not be saved. Try again shortly.";
    default:
      return "The permission change could not be saved.";
  }
}
