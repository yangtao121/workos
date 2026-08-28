import { Code, ConnectError } from "@connectrpc/connect";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  DeviceClass,
  SurfaceRenderer,
  type AppInstallation,
  type Project,
  type SurfaceSession,
  type WorkOSApp,
} from "@workos/protocol";
import { Button } from "@workos/ui-kit";
import type { WorkOSClients } from "@workos/agent-sdk";

export type LibraryState = "loading" | "ready" | "error";

interface AppLibraryProps {
  project: Project;
  workosClients: Pick<WorkOSClients, "appRegistry" | "appInstallations" | "projects" | "surfaces">;
  onProjectRefreshed: (project: Project) => void;
  onSurfaceOpened: (session: SurfaceSession) => void;
  onInstallationRemoved: (installationId: string) => void;
}

interface LibraryFeedback {
  text: string;
  isError: boolean;
}

// AppLibrary lists the owner's registry catalog for the active project,
// marks active installations with their pinned version, and installs or
// removes apps through the public AppInstallationService. It never guesses
// local state: after every mutation it re-reads the project (server-owned
// revision) and the installation list.
export function AppLibrary({
  project,
  workosClients,
  onProjectRefreshed,
  onSurfaceOpened,
  onInstallationRemoved,
}: AppLibraryProps) {
  const [state, setState] = useState<LibraryState>("loading");
  const [error, setError] = useState<string>();
  const [apps, setApps] = useState<WorkOSApp[]>([]);
  const [installations, setInstallations] = useState<AppInstallation[]>([]);
  const [busyAppIds, setBusyAppIds] = useState<Record<string, boolean>>({});
  const [feedback, setFeedback] = useState<LibraryFeedback>();
  const [openingAppIds, setOpeningAppIds] = useState<Record<string, boolean>>({});
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

  const refreshProjectAndInstallations = useCallback(
    async (isLive: () => boolean) => {
      const [projectResponse, active] = await Promise.all([
        workosClients.projects.getProject({ projectId: project.id }),
        listAllInstallations(workosClients, project.id),
      ]);
      const refreshed = projectResponse.project;
      if (!refreshed) throw new Error("missing refreshed project");
      if (!isLive()) return;
      onProjectRefreshed(refreshed);
      setInstallations(active);
    },
    [onProjectRefreshed, project.id, workosClients],
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

  const install = useCallback(
    (app: WorkOSApp) => {
      void runMutation(app.id, async (revision) => {
        await workosClients.appInstallations.installApp({
          idempotencyKey: crypto.randomUUID(),
          projectId: project.id,
          appId: app.id,
          version: "",
          expectedProjectRevision: revision,
        });
      });
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

  // Open launches one installed instance as a surface. The idempotency key is
  // fresh per click, the identity comes only from the gateway session, and
  // stale responses after a project switch or unmount are inert — a late
  // session is closed best-effort because nothing renders it anymore.
  const open = useCallback(
    (installation: AppInstallation) => {
      if (openingAppIds[installation.appId]) return;
      setOpeningAppIds((current) => ({ ...current, [installation.appId]: true }));
      setFeedback(undefined);
      const generation = generationRef.current;
      const isLive = () => generation === generationRef.current;
      void workosClients.surfaces
        .createSurface({
          idempotencyKey: crypto.randomUUID(),
          appInstanceId: installation.id,
          projectId: project.id,
          deviceClass: DeviceClass.DESKTOP,
          viewport: {
            width: window.innerWidth,
            height: window.innerHeight,
            pixelRatio: window.devicePixelRatio,
          },
          preferredRenderer: SurfaceRenderer.WEB_BUNDLE,
        })
        .then((response) => {
          const session = response.session;
          if (!session) throw new Error("missing surface session");
          if (!isLive()) {
            void workosClients.surfaces
              .closeSurface({ surfaceSessionId: session.id })
              .catch(() => undefined);
            return;
          }
          onSurfaceOpened(session);
        })
        .catch((reason: unknown) => {
          if (isLive()) {
            setFeedback({ text: openErrorMessage(reason), isError: true });
          }
        })
        .finally(() => {
          if (isLive()) {
            setOpeningAppIds((current) => ({ ...current, [installation.appId]: false }));
          }
        });
    },
    [onSurfaceOpened, openingAppIds, project.id, workosClients],
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
                    <small>
                      Installed · pinned {installation.version} ·{" "}
                      {shortDigest(installation.manifestDigest)}
                    </small>
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
                      install(app);
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
    </section>
  );
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
      return "This app version has no supported web bundle, so it cannot be opened yet.";
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
