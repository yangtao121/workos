import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  type DeviceClass,
  SurfaceRenderer,
  type AppInstallation,
  type Project,
  type SurfaceSession,
  type WorkOSApp,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import type { WorkOSClients } from "@workos/agent-sdk";
import { PermissionDialog, type PermissionFacts } from "./PermissionDialog.js";
import { PolicyDialog, policySummaryLabel } from "./PolicyDialog.js";

export type LibraryState = "loading" | "ready" | "error";

interface AppLibraryProps {
  project: Project;
  // The canonical proto device class of the current device, derived by the
  // shared adaptive-shell contract (never guessed from a user agent).
  deviceClass: DeviceClass;
  workosClients: Pick<
    WorkOSClients,
    "appRegistry" | "appInstallations" | "projects" | "surfaces" | "appPolicies"
  >;
  onProjectRefreshed: (project: Project) => void;
  onSurfaceOpened: (session: SurfaceSession) => void;
  onInstallationRemoved: (installationId: string) => void;
  // Fired once a SetAppGrants save was server-confirmed: the Desktop tears
  // down every still-open window/session of exactly that installation.
  onInstallationGrantsChanged?: (installationId: string) => void;
}

interface LibraryFeedback {
  text: string;
  isError: boolean;
}

interface InstallConsent {
  app: WorkOSApp;
  selected: string[];
  submitting: boolean;
}

// Grants are stored canonically sorted, so the dialog submits the sorted
// selection and the server validates the rest.
function sortedCapabilities(values: string[]): string[] {
  return [...values].sort((left, right) => left.localeCompare(right));
}

// AppLibrary lists the owner's registry catalog for the active project,
// marks active installations with their pinned version, and installs or
// removes apps through the public AppInstallationService. It never guesses
// local state: after every mutation it re-reads the project (server-owned
// revision) and the installation list.
export function AppLibrary({
  project,
  deviceClass,
  workosClients,
  onProjectRefreshed,
  onSurfaceOpened,
  onInstallationRemoved,
  onInstallationGrantsChanged,
}: AppLibraryProps) {
  const [state, setState] = useState<LibraryState>("loading");
  const [error, setError] = useState<string>();
  const [apps, setApps] = useState<WorkOSApp[]>([]);
  const [installations, setInstallations] = useState<AppInstallation[]>([]);
  const [busyAppIds, setBusyAppIds] = useState<Record<string, boolean>>({});
  const [feedback, setFeedback] = useState<LibraryFeedback>();
  const [openingAppIds, setOpeningAppIds] = useState<Record<string, boolean>>({});
  // The install consent flow: the exact registry version's requested
  // permissions are listed and every checkbox starts unchecked; the user's
  // explicit selection becomes the installation's first grant.
  const [consent, setConsent] = useState<InstallConsent>();
  // The permission dialog edits one installation's grant as a full
  // replacement (ADR-0003). The captured installation is kept stable for the
  // dialog's lifetime; the dialog reloads fresh facts itself on conflicts.
  const [managing, setManaging] = useState<AppInstallation>();
  // The Agent policy dialog edits one installation's execution policy
  // (ADR-0005); the per-installation summaries render on the installed rows.
  const [policyManaging, setPolicyManaging] = useState<AppInstallation>();
  const [policySummaries, setPolicySummaries] = useState<Record<string, string>>({});
  // Generation guards every in-flight promise: switching projects or
  // unmounting invalidates late responses so they cannot pollute state.
  const generationRef = useRef(0);
  const projectIdRef = useRef(project.id);
  projectIdRef.current = project.id;
  useEffect(() => {
    return () => {
      generationRef.current += 1;
    };
  }, []);

  const refresh = useCallback(async () => {
    const generation = generationRef.current;
    const isLive = () => generation === generationRef.current;
    setState("loading");
    setError(undefined);
    try {
      const [catalog, active] = await Promise.all([
        listAllCatalogApps(workosClients, project.id),
        listAllInstallations(workosClients, project.id),
      ]);
      if (!isLive()) return;
      setApps(catalog);
      setInstallations(active);
      setState("ready");
      refreshPolicySummaries(active);
    } catch {
      if (!isLive()) return;
      setApps([]);
      setInstallations([]);
      setState("error");
      setError("The app library is temporarily unavailable.");
    }
  }, [project.id, workosClients]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // refreshPolicySummaries resolves the effective policy label per installed
  // row. Failures degrade to the placeholder, never to a wrong label.
  const refreshPolicySummaries = useCallback(
    (active: AppInstallation[]) => {
      const generation = generationRef.current;
      void Promise.all(
        active.map(async (installation) => {
          try {
            const response = await workosClients.appPolicies.getAppPolicy({
              projectId: project.id,
              installationId: installation.id,
            });
            return response.policy ? policySummaryLabel(response.policy) : "";
          } catch {
            return "";
          }
        }),
      ).then((summaries) => {
        if (generation !== generationRef.current) return;
        const next: Record<string, string> = {};
        active.forEach((installation, index) => {
          if (summaries[index]) next[installation.id] = summaries[index];
        });
        setPolicySummaries(next);
      });
    },
    [project.id, workosClients],
  );

  // readFacts re-reads the server-owned project revision and installation
  // list; callers apply them under their own liveness guard so the permission
  // dialog can adopt the same fresh facts without duplicating the walk.
  const readFacts = useCallback(async (): Promise<PermissionFacts> => {
    const [projectResponse, active] = await Promise.all([
      workosClients.projects.getProject({ projectId: project.id }),
      listAllInstallations(workosClients, project.id),
    ]);
    const refreshed = projectResponse.project;
    if (!refreshed) throw new Error("missing refreshed project");
    return { project: refreshed, installations: active };
  }, [project.id, workosClients]);

  const applyFacts = useCallback(
    (facts: PermissionFacts) => {
      onProjectRefreshed(facts.project);
      setInstallations(facts.installations);
      // Install/uninstall/grant flows replace the installation list without
      // a full refresh; policy labels follow the fresh list.
      refreshPolicySummaries(facts.installations);
    },
    [onProjectRefreshed, refreshPolicySummaries],
  );

  // A save whose post-save re-read failed still carries the server truth in
  // its Set response: merge that confirmed installation in place so the row
  // shows the saved grant, and forward only the revision. Every other project
  // field stays from the last complete read — the Set response does not carry
  // them, but the revision is the server fact it does carry.
  const installationSaved = useCallback(
    (saved: AppInstallation, savedRevision: bigint) => {
      setInstallations((current) => current.map((item) => (item.id === saved.id ? saved : item)));
      onProjectRefreshed({ ...project, revision: savedRevision });
    },
    [onProjectRefreshed, project],
  );

  const refreshProjectAndInstallations = useCallback(
    async (isLive: () => boolean) => {
      const facts = await readFacts();
      if (!isLive()) return;
      applyFacts(facts);
    },
    [applyFacts, readFacts],
  );

  // Opening the permission editor always resolves fresh server facts first:
  // the row being edited may be a stale cache entry from before a grant
  // change in another tab or on another device, and both the checkbox seed
  // and the Save's expected revision must describe the installation's
  // current grant, not the moment this library last listed it.
  const openingManageRef = useRef(false);
  const openManaging = useCallback(
    async (row: AppInstallation) => {
      if (openingManageRef.current) return;
      openingManageRef.current = true;
      try {
        const facts = await readFacts();
        const fresh = facts.installations.find((item) => item.id === row.id);
        if (!fresh) {
          // The installation vanished (e.g. uninstalled elsewhere): adopt the
          // fresh facts and never open an editor for a gone installation.
          applyFacts(facts);
          setFeedback({ text: "The app is no longer installed.", isError: true });
          return;
        }
        applyFacts(facts);
        setManaging(fresh);
      } catch {
        // Facts unavailable: edit from the cached row. The server still
        // arbitrates the Save, and a revision conflict reloads fresh facts.
        setManaging(row);
      } finally {
        openingManageRef.current = false;
      }
    },
    [applyFacts, readFacts],
  );

  const runMutation = useCallback(
    async (appId: string, mutation: (revision: bigint) => Promise<void>) => {
      setBusyAppIds((current) => ({ ...current, [appId]: true }));
      setFeedback(undefined);
      const generation = generationRef.current;
      const isLive = () => generation === generationRef.current;
      try {
        await mutation(project.revision);
        await refreshProjectAndInstallations(isLive);
      } catch (reason) {
        if (!isLive()) return;
        if (reason instanceof ConnectError && reason.code === Code.Aborted) {
          // The revision moved: reload the server state and tell the user
          // instead of silently replaying their mutation.
          try {
            await refreshProjectAndInstallations(isLive);
            if (isLive()) {
              setFeedback({
                text: "Project settings changed elsewhere. The latest revision was loaded.",
                isError: true,
              });
            }
          } catch {
            if (isLive()) {
              setFeedback({
                text: "Project settings changed elsewhere and could not be refreshed.",
                isError: true,
              });
            }
          }
        } else if (isLive()) {
          setFeedback({ text: installErrorMessage(reason), isError: true });
        }
      } finally {
        if (isLive()) {
          setBusyAppIds((current) => ({ ...current, [appId]: false }));
        }
      }
    },
    [project.revision, refreshProjectAndInstallations],
  );

  // Install opens the consent dialog for the exact registry version on
  // display: the user sees that version's requested permissions and the
  // server pins exactly that version, so the approved set can never drift
  // from the installed one.
  const confirmInstall = useCallback(
    (consentState: InstallConsent) => {
      setConsent({ ...consentState, submitting: true });
      void runMutation(consentState.app.id, async (revision) => {
        await workosClients.appInstallations.installApp({
          idempotencyKey: crypto.randomUUID(),
          projectId: project.id,
          appId: consentState.app.id,
          // Explicit version: what the user approved is what gets installed.
          version: consentState.app.version,
          expectedProjectRevision: revision,
          grantedPermissions: sortedCapabilities(consentState.selected),
        });
      });
      setConsent(undefined);
    },
    [project.id, runMutation, workosClients],
  );

  const remove = useCallback(
    (installation: AppInstallation) => {
      void runMutation(installation.appId, async (revision) => {
        await workosClients.appInstallations.uninstallApp({
          idempotencyKey: crypto.randomUUID(),
          projectId: project.id,
          installationId: installation.id,
          expectedProjectRevision: revision,
        });
        onInstallationRemoved(installation.id);
      });
    },
    [onInstallationRemoved, project.id, runMutation, workosClients],
  );

  // Open launches one installed instance as a surface through the shared
  // open helper: the idempotency key is fresh per click, the identity comes
  // only from the gateway session, and stale responses after a project
  // switch or unmount are inert — a late session is closed best-effort
  // because nothing renders it anymore.
  const open = useCallback(
    (installation: AppInstallation) => {
      if (openingAppIds[installation.appId]) return;
      setOpeningAppIds((current) => ({ ...current, [installation.appId]: true }));
      setFeedback(undefined);
      const generation = generationRef.current;
      const isLive = () => generation === generationRef.current;
      void openInstallationSurface(
        workosClients,
        project.id,
        installation.id,
        deviceClass,
        () => isLive(),
        (session) => {
          onSurfaceOpened(session);
        },
        (reason: unknown) => {
          if (isLive()) {
            setFeedback({ text: openErrorMessage(reason), isError: true });
          }
        },
      ).finally(() => {
        if (isLive()) {
          setOpeningAppIds((current) => ({ ...current, [installation.appId]: false }));
        }
      });
    },
    [deviceClass, onSurfaceOpened, openingAppIds, project.id, workosClients],
  );

  const installationByApp = new Map(installations.map((item) => [item.appId, item]));
  const installedButUnknown = installations.filter(
    (item) => !apps.some((app) => app.id === item.appId),
  );

  return (
    <section className="app-library" aria-labelledby="app-library-title">
      <header>
        <div>
          <p>PROJECT APPS</p>
          <h2 id="app-library-title">App Library</h2>
        </div>
        <span>revision {project.revision.toString()}</span>
      </header>

      {state === "loading" ? (
        <p className="library-state" role="status">
          Loading app library…
        </p>
      ) : null}

      {state === "error" ? (
        <div className="library-state library-error" role="alert">
          <p>{error || "The app library is unavailable."}</p>
          <Button onClick={() => void refresh()} type="button">
            Retry library
          </Button>
        </div>
      ) : null}

      {state === "ready" && apps.length === 0 && installedButUnknown.length === 0 ? (
        <p className="library-state">No apps have been registered yet.</p>
      ) : null}

      {feedback ? (
        <p
          className={feedback.isError ? "library-feedback error" : "library-feedback"}
          role={feedback.isError ? "alert" : "status"}
        >
          {feedback.text}
        </p>
      ) : null}

      {state === "ready" ? (
        <ul className="app-list" aria-label="Registered apps">
          {apps.map((app) => {
            const installation = installationByApp.get(app.id);
            const busy = busyAppIds[app.id] ?? false;
            return (
              <li className={installation ? "app-row installed" : "app-row"} key={app.id}>
                <span>
                  <strong>{app.name || app.id}</strong>
                  <small>
                    {app.id} · registry {app.version}
                  </small>
                  {installation ? (
                    <>
                      <small>
                        Installed · pinned {installation.version} ·{" "}
                        {shortDigest(installation.manifestDigest)}
                      </small>
                      <small className="app-row-granted">
                        Granted: {grantedSummary(installation.grantedPermissions)} · grant revision{" "}
                        {installation.grantRevision.toString()}
                      </small>
                      <small className="app-row-policy">
                        Agent policy: {policySummaries[installation.id] ?? "…"}
                      </small>
                    </>
                  ) : null}
                </span>
                {installation ? (
                  <span className="app-row-actions">
                    <Button
                      disabled={busy || openingAppIds[app.id] === true}
                      onClick={() => {
                        open(installation);
                      }}
                      type="button"
                    >
                      {openingAppIds[app.id] === true ? "Opening…" : "Open"}
                    </Button>
                    <Button
                      disabled={busy || openingAppIds[app.id] === true}
                      onClick={() => {
                        setFeedback(undefined);
                        void openManaging(installation);
                      }}
                      type="button"
                    >
                      Manage permissions
                    </Button>
                    <Button
                      disabled={busy || openingAppIds[app.id] === true}
                      onClick={() => {
                        setFeedback(undefined);
                        setPolicyManaging(installation);
                      }}
                      type="button"
                    >
                      Agent policy
                    </Button>
                    <Button
                      disabled={busy || openingAppIds[app.id] === true}
                      onClick={() => {
                        remove(installation);
                      }}
                      type="button"
                    >
                      {busy ? "Removing…" : "Remove"}
                    </Button>
                  </span>
                ) : (
                  <Button
                    disabled={busy}
                    onClick={() => {
                      setConsent({ app, selected: [], submitting: false });
                    }}
                    type="button"
                  >
                    {busy ? "Installing…" : "Install"}
                  </Button>
                )}
              </li>
            );
          })}
          {installedButUnknown.map((installation) => (
            <li className="app-row installed" key={installation.id}>
              <span>
                <strong>{installation.appId}</strong>
                <small>
                  Installed · pinned {installation.version} ·{" "}
                  {shortDigest(installation.manifestDigest)}
                </small>
                <small className="app-row-granted">
                  Granted: {grantedSummary(installation.grantedPermissions)} · grant revision{" "}
                  {installation.grantRevision.toString()}
                </small>
                {/* The app is absent from the owner's current catalog, so its
                    exact pinned requested set cannot be resolved here: never
                    guess one. Removing stays available. */}
                <small>Manage permissions unavailable</small>
              </span>
              <Button
                disabled={busyAppIds[installation.appId] ?? false}
                onClick={() => {
                  remove(installation);
                }}
                type="button"
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
      ) : null}

      {consent ? (
        <div className="consent-backdrop" role="presentation">
          <div
            aria-describedby="app-consent-description"
            aria-labelledby="app-consent-title"
            aria-modal="true"
            className="app-consent"
            role="dialog"
          >
            <h3 id="app-consent-title">
              Install {consent.app.name || consent.app.id} {consent.app.version}
            </h3>
            <p id="app-consent-description">
              Requested permissions for registry version {consent.app.version}. Nothing is granted
              by default; you can change these later from Manage permissions.
            </p>
            {consent.app.permissions.length === 0 ? (
              <p className="app-consent-note">This app requests no permissions.</p>
            ) : (
              <ul className="app-consent-list">
                {consent.app.permissions.map((permission) => (
                  <li key={permission}>
                    <label>
                      <input
                        aria-label={permission}
                        checked={consent.selected.includes(permission)}
                        onChange={(event) => {
                          setConsent((current) => {
                            if (!current) return current;
                            return event.target.checked
                              ? { ...current, selected: current.selected.concat(permission) }
                              : {
                                  ...current,
                                  selected: current.selected.filter((id) => id !== permission),
                                };
                          });
                        }}
                        type="checkbox"
                      />
                      <span>{permission}</span>
                    </label>
                  </li>
                ))}
              </ul>
            )}
            <div className="app-consent-actions">
              <Button
                disabled={consent.submitting}
                onClick={() => {
                  confirmInstall(consent);
                }}
                type="button"
              >
                {consent.selected.length === 0
                  ? "Install without permissions"
                  : `Install with ${String(consent.selected.length)} permission${consent.selected.length === 1 ? "" : "s"}`}
              </Button>
              <Button
                disabled={consent.submitting}
                onClick={() => {
                  setConsent(undefined);
                }}
                type="button"
              >
                Cancel
              </Button>
            </div>
          </div>
        </div>
      ) : null}

      {policyManaging ? (
        <PolicyDialog
          installation={policyManaging}
          key={policyManaging.id}
          onClose={() => {
            setPolicyManaging(undefined);
          }}
          onSaved={(installationId, policy) => {
            setPolicySummaries((current) => ({
              ...current,
              [installationId]: policySummaryLabel(policy),
            }));
          }}
          workosClients={workosClients}
        />
      ) : null}

      {managing ? (
        <PermissionDialog
          installation={managing}
          key={managing.id}
          onCancel={() => {
            setManaging(undefined);
          }}
          onFactsRefreshed={applyFacts}
          onGrantsApplied={(installationId) => {
            onInstallationGrantsChanged?.(installationId);
          }}
          onInstallationSaved={installationSaved}
          project={project}
          readFacts={readFacts}
          workosClients={workosClients}
        />
      ) : null}
    </section>
  );
}

// openInstallationSurface is the single CreateSurface flow for opening an
// installed instance: the identity comes only from the gateway session, the
// device class is the caller's derived proto fact, and a session that
// returns after its caller lost interest is closed best-effort instead of
// rendering into a stale view.
export async function openInstallationSurface(
  workosClients: Pick<WorkOSClients, "surfaces">,
  projectId: string,
  installationId: string,
  deviceClass: DeviceClass,
  isLive: () => boolean,
  onOpened: (session: SurfaceSession) => void,
  onError: (reason: unknown) => void,
): Promise<void> {
  try {
    const response = await workosClients.surfaces.createSurface({
      idempotencyKey: crypto.randomUUID(),
      appInstanceId: installationId,
      projectId,
      deviceClass,
      viewport: {
        width: window.innerWidth,
        height: window.innerHeight,
        pixelRatio: window.devicePixelRatio,
      },
      // The server selects the renderer from the exact installed
      // descriptor: web bundles open as before, supervised container apps
      // start their workload first.
      preferredRenderer: SurfaceRenderer.UNSPECIFIED,
    });
    const session = response.session;
    if (!session) throw new Error("missing surface session");
    if (!isLive()) {
      void workosClients.surfaces
        .closeSurface({ surfaceSessionId: session.id })
        .catch(() => undefined);
      return;
    }
    onOpened(session);
  } catch (reason: unknown) {
    onError(reason);
  }
}

function grantedSummary(granted: readonly string[] | undefined): string {
  if (!granted || granted.length === 0) return "none";
  return granted.join(", ");
}

// listAllCatalogApps walks the owner's registry catalog pages in the project
// context. Installations walk their own pages the same way.
async function listAllCatalogApps(
  workosClients: Pick<WorkOSClients, "appRegistry">,
  projectId: string,
): Promise<WorkOSApp[]> {
  const apps: WorkOSApp[] = [];
  let token = "";
  for (;;) {
    const page = await workosClients.appRegistry.listApps({
      projectId,
      page: { pageSize: 100, pageToken: token },
    });
    apps.push(...page.apps);
    if (page.page?.nextPageToken === "") break;
    token = page.page?.nextPageToken ?? "";
  }
  return apps;
}

async function listAllInstallations(
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

function shortDigest(digest: string): string {
  return digest.length > 19 ? `${digest.slice(0, 19)}…` : digest;
}

function openErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The app surface could not be opened.";
  switch (reason.code) {
    case Code.NotFound:
      return "The installed app or project is no longer available.";
    case Code.FailedPrecondition:
      return "This app version has no supported surface renderer in this deployment, so it cannot be opened yet.";
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return "The surface runtime is temporarily unavailable. Try again shortly.";
    default:
      return "The app surface could not be opened.";
  }
}

function installErrorMessage(reason: unknown): string {
  if (!(reason instanceof ConnectError)) return "The app change could not be saved.";
  switch (reason.code) {
    case Code.AlreadyExists:
      return "A different version of this app is already installed. Upgrades are not part of this release.";
    case Code.NotFound:
      return "The app or project is no longer available.";
    case Code.InvalidArgument:
      return "The app change was rejected. Reload and try again.";
    default:
      return "The app change could not be saved.";
  }
}
