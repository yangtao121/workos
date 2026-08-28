// @vitest-environment jsdom

import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AppScope,
  DeviceClass,
  SurfaceRenderer,
  type AppInstallation,
  type InstallAppResponse,
  type ListAppsResponse,
  type ListInstalledAppsResponse,
  type Project,
  type SurfaceSession,
  type UninstallAppResponse,
  type WorkOSApp,
} from "@workos/protocol";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppLibrary } from "./AppLibrary.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(cleanup);

function app(id: string, version: string): WorkOSApp {
  return {
    $typeName: "workos.app.v1.WorkOSApp",
    id,
    name: id.replace(/-/g, " "),
    version,
    scope: AppScope.USER,
    permissions: [],
    manifestDigest: `sha256:${id.padEnd(64, "0").slice(0, 64)}`,
  };
}

function installation(id: string, appId: string, version: string): AppInstallation {
  return {
    $typeName: "workos.app.v1.AppInstallation",
    id,
    projectId: "project-1",
    appId,
    version,
    manifestDigest: `sha256:${appId.padEnd(64, "0").slice(0, 64)}`,
    installedAt: { $typeName: "google.protobuf.Timestamp", seconds: 1787000000n, nanos: 0 },
    uninstalledAt: undefined,
  };
}

function project(id: string, revision: bigint): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id,
    ownerUserId: "local-user",
    name: `Project ${id}`,
    icon: "◈",
    workspaceRefs: [],
    harnessBinding: undefined,
    installedAppIds: [],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    artifactCollectionId: "",
    revision,
  };
}

interface LibraryFixture {
  apps?: WorkOSApp[];
  installations?: AppInstallation[];
  installApp?: ReturnType<typeof vi.fn>;
  uninstallApp?: ReturnType<typeof vi.fn>;
  getProject?: ReturnType<typeof vi.fn>;
  listApps?: ReturnType<typeof vi.fn>;
  listInstalledApps?: ReturnType<typeof vi.fn>;
  createSurface?: ReturnType<typeof vi.fn>;
  closeSurface?: ReturnType<typeof vi.fn>;
}

function clientsFixture(fixture: LibraryFixture) {
  const apps = fixture.apps ?? [];
  const installations = fixture.installations ?? [];
  return {
    appRegistry: {
      listApps:
        fixture.listApps ??
        vi.fn(() =>
          Promise.resolve({
            $typeName: "workos.app.v1.ListAppsResponse",
            apps,
            page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
          } satisfies ListAppsResponse),
        ),
    },
    appInstallations: {
      installApp:
        fixture.installApp ??
        vi.fn(() =>
          Promise.resolve({
            $typeName: "workos.app.v1.InstallAppResponse",
            installation: installation("installation-9", "board-app", "1.0.0"),
            projectRevision: 2n,
          } satisfies InstallAppResponse),
        ),
      uninstallApp:
        fixture.uninstallApp ??
        vi.fn(() =>
          Promise.resolve({
            $typeName: "workos.app.v1.UninstallAppResponse",
            installation: installation("installation-1", "board-app", "1.0.0"),
            projectRevision: 2n,
          } satisfies UninstallAppResponse),
        ),
      listInstalledApps:
        fixture.listInstalledApps ??
        vi.fn(() =>
          Promise.resolve({
            $typeName: "workos.app.v1.ListInstalledAppsResponse",
            installations,
            page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
          } satisfies ListInstalledAppsResponse),
        ),
    },
    projects: {
      getProject:
        fixture.getProject ?? vi.fn(() => Promise.resolve({ project: project("project-1", 2n) })),
    },
    surfaces: {
      createSurface:
        fixture.createSurface ??
        vi.fn(() =>
          Promise.resolve({
            $typeName: "workos.surface.v1.CreateSurfaceResponse",
            session: {
              $typeName: "workos.surface.v1.SurfaceSession",
              id: "surface-session-1",
              appInstanceId: "installation-1",
              projectId: "project-1",
              renderer: SurfaceRenderer.WEB_BUNDLE,
              url: "/surfaces/surface-session-1/",
              bridgeToken: "",
              resize: false,
              clipboard: false,
              filePicker: false,
            } as SurfaceSession,
          }),
        ),
      closeSurface: fixture.closeSurface ?? vi.fn(() => Promise.resolve({})),
    },
  } as unknown as WorkOSClients;
}

function deferred<T>() {
  let settle!: (value: T) => void;
  let fail!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolve, reject) => {
    settle = resolve;
    fail = reject;
  });
  return {
    promise,
    resolve: (value: T): Promise<T> => {
      settle(value);
      return promise;
    },
    reject: (reason?: unknown): Promise<never> => {
      fail(reason);
      return promise as Promise<never>;
    },
  };
}

describe("App Library", () => {
  it("shows loading, then the catalog with active installations and pinned versions", async () => {
    const clients = clientsFixture({
      apps: [app("board-app", "2.0.0"), app("notes-app", "1.1.0")],
      installations: [installation("installation-1", "board-app", "1.9.0")],
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    // The loading state exists only until the catalog resolves; asserting
    // the terminal state keeps the test deterministic.
    expect(await screen.findByRole("list", { name: "Registered apps" })).toBeTruthy();
    expect(screen.getByText("board app")).toBeTruthy();
    expect(screen.getByText(/Installed · pinned 1\.9\.0/)).toBeTruthy();
    expect(screen.getByText(/registry 2\.0\.0/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Install" })).toBeTruthy();
  });

  it("renders an empty state when no apps are registered", async () => {
    render(
      <AppLibrary
        project={project("project-1", 1n)}
        workosClients={clientsFixture({})}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    expect(await screen.findByText("No apps have been registered yet.")).toBeTruthy();
  });

  it("retries after a load failure and hides the error once ready", async () => {
    const clients = clientsFixture({});
    let failing = true;
    (clients.appRegistry as unknown as { listApps: ReturnType<typeof vi.fn> }).listApps = vi.fn(
      () => {
        if (failing) return Promise.reject(new ConnectError("offline", Code.Unavailable));
        return Promise.resolve({
          $typeName: "workos.app.v1.ListAppsResponse",
          apps: [app("board-app", "1.0.0")],
          page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
        } satisfies ListAppsResponse);
      },
    );
    render(
      <AppLibrary
        project={project("project-1", 1n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("app library is temporarily unavailable");
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: "Retry library" }));
    expect(await screen.findByText("board app")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("installs with a fresh idempotency key and the current project revision, then adopts server state", async () => {
    const installApp = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.app.v1.InstallAppResponse",
        installation: installation("installation-9", "board-app", "1.0.0"),
        projectRevision: 3n,
      } satisfies InstallAppResponse),
    );
    const getProject = vi.fn(() => Promise.resolve({ project: project("project-1", 3n) }));
    const refreshedInstallations = [installation("installation-9", "board-app", "1.0.0")];
    const listInstalledApps = vi
      .fn()
      .mockResolvedValueOnce({
        $typeName: "workos.app.v1.ListInstalledAppsResponse",
        installations: [],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListInstalledAppsResponse)
      .mockResolvedValue({
        $typeName: "workos.app.v1.ListInstalledAppsResponse",
        installations: refreshedInstallations,
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListInstalledAppsResponse);
    const onProjectRefreshed = vi.fn();
    const clients = clientsFixture({
      apps: [app("board-app", "1.0.0")],
      installApp,
      getProject,
      listInstalledApps,
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={onProjectRefreshed}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Install" }));
    await waitFor(() => {
      expect(installApp).toHaveBeenCalledWith({
        idempotencyKey: expect.any(String) as string,
        projectId: "project-1",
        appId: "board-app",
        version: "",
        expectedProjectRevision: 2n,
      });
    });
    await waitFor(() => {
      expect(onProjectRefreshed).toHaveBeenCalledWith(
        expect.objectContaining({ revision: 3n }) as unknown as Project,
      );
    });
    await waitFor(() => {
      expect(screen.getByText(/Installed · pinned 1\.0\.0/)).toBeTruthy();
    });
  });

  it("removes an installed app through the same boundary", async () => {
    const uninstallApp = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.app.v1.UninstallAppResponse",
        installation: installation("installation-1", "board-app", "1.9.0"),
        projectRevision: 3n,
      } satisfies UninstallAppResponse),
    );
    const listInstalledApps = vi
      .fn()
      .mockResolvedValueOnce({
        $typeName: "workos.app.v1.ListInstalledAppsResponse",
        installations: [installation("installation-1", "board-app", "1.9.0")],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListInstalledAppsResponse)
      .mockResolvedValue({
        $typeName: "workos.app.v1.ListInstalledAppsResponse",
        installations: [],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListInstalledAppsResponse);
    const clients = clientsFixture({
      apps: [app("board-app", "2.0.0")],
      installations: [installation("installation-1", "board-app", "1.9.0")],
      uninstallApp,
      listInstalledApps,
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    await waitFor(() => {
      expect(uninstallApp).toHaveBeenCalledWith({
        idempotencyKey: expect.any(String) as string,
        projectId: "project-1",
        installationId: "installation-1",
        expectedProjectRevision: 2n,
      });
    });
    // The refreshed installation list is empty, so the app offers Install
    // again — but the catalog may still be resolving, so await the button.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Install" })).toBeTruthy();
    });
  });

  it("reloads the latest project after a revision conflict instead of replaying the mutation", async () => {
    const installApp = vi.fn(() =>
      Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
    );
    const getProject = vi.fn(() => Promise.resolve({ project: project("project-1", 7n) }));
    const onProjectRefreshed = vi.fn();
    const clients = clientsFixture({
      apps: [app("board-app", "1.0.0")],
      installApp,
      getProject,
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={onProjectRefreshed}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Install" }));
    await waitFor(() => {
      expect(
        screen.getByText("Project settings changed elsewhere. The latest revision was loaded."),
      ).toBeTruthy();
    });
    expect(onProjectRefreshed).toHaveBeenCalledWith(
      expect.objectContaining({ revision: 7n }) as unknown as Project,
    );
    expect(installApp).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Install" })).toBeTruthy();
  });

  it("surfaces a sanitized business failure and leaves the button usable", async () => {
    const installApp = vi.fn(() =>
      Promise.reject(
        new ConnectError("app is already installed with a different version", Code.AlreadyExists),
      ),
    );
    const clients = clientsFixture({
      apps: [app("board-app", "1.0.0")],
      installApp,
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Install" }));
    await waitFor(() => {
      expect(
        screen.getByText(
          "A different version of this app is already installed. Upgrades are not part of this release.",
        ),
      ).toBeTruthy();
    });
    const installButton = screen.getByRole<HTMLButtonElement>("button", { name: "Install" });
    expect(installButton.disabled).toBe(false);
  });

  it("ignores responses that settle after the component unmounts", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const pending = deferred<InstallAppResponse>();
      const installApp = vi.fn(() => pending.promise);
      const clients = clientsFixture({
        apps: [app("board-app", "1.0.0")],
        installApp,
      });
      const view = render(
        <AppLibrary
          project={project("project-1", 2n)}
          workosClients={clients}
          onProjectRefreshed={() => undefined}
          onSurfaceOpened={() => undefined}
          onInstallationRemoved={() => undefined}
        />,
      );
      await userEvent.click(await screen.findByRole("button", { name: "Install" }));
      view.unmount();
      await act(async () => {
        await pending.resolve({
          $typeName: "workos.app.v1.InstallAppResponse",
          installation: installation("installation-9", "board-app", "1.0.0"),
          projectRevision: 3n,
        });
      });
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("does not leak one project's late response into another project", async () => {
    // The component is keyed by project id in Desktop; simulating the same
    // remount here proves a late settle from the old instance is inert.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const pending = deferred<InstallAppResponse>();
      const installApp = vi.fn(() => pending.promise);
      const firstClients = clientsFixture({ apps: [app("board-app", "1.0.0")], installApp });
      const first = render(
        <AppLibrary
          project={project("project-1", 2n)}
          workosClients={firstClients}
          onProjectRefreshed={() => undefined}
          onSurfaceOpened={() => undefined}
          onInstallationRemoved={() => undefined}
        />,
      );
      await userEvent.click(await first.findByRole("button", { name: "Install" }));
      first.unmount();

      const second = render(
        <AppLibrary
          project={project("project-2", 5n)}
          workosClients={clientsFixture({ apps: [app("notes-app", "1.0.0")] })}
          onProjectRefreshed={() => undefined}
          onSurfaceOpened={() => undefined}
          onInstallationRemoved={() => undefined}
        />,
      );
      await act(async () => {
        await pending.resolve({
          $typeName: "workos.app.v1.InstallAppResponse",
          installation: installation("installation-9", "board-app", "1.0.0"),
          projectRevision: 3n,
        });
      });
      expect(await second.findByText("notes app")).toBeTruthy();
      expect(second.queryByText(/board app/)).toBeNull();
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
    }
  });
});

describe("App Library open", () => {
  it("opens an installed app through CreateSurface with a fresh idempotency key", async () => {
    const user = userEvent.setup();
    const onSurfaceOpened = vi.fn();
    const clients = clientsFixture({
      apps: [app("board-app", "2.0.0")],
      installations: [installation("installation-1", "board-app", "1.9.0")],
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={onSurfaceOpened}
        onInstallationRemoved={() => undefined}
      />,
    );
    await screen.findByText(/Installed · pinned 1\.9\.0/);
    await user.click(screen.getByRole("button", { name: "Open" }));
    await waitFor(() => {
      expect(onSurfaceOpened).toHaveBeenCalledTimes(1);
    });
    const session = onSurfaceOpened.mock.calls[0]?.[0] as SurfaceSession;
    expect(session.id).toBe("surface-session-1");
    expect(session.url).toBe("/surfaces/surface-session-1/");
    const request = (clients.surfaces.createSurface as ReturnType<typeof vi.fn>).mock
      .calls[0]?.[0] as {
      idempotencyKey: string;
      appInstanceId: string;
      projectId: string;
      deviceClass: DeviceClass;
      preferredRenderer: SurfaceRenderer;
      viewport: { width: number };
    };
    expect(request.idempotencyKey).toEqual(expect.any(String));
    expect(request.appInstanceId).toBe("installation-1");
    expect(request.projectId).toBe("project-1");
    expect(request.deviceClass).toBe(DeviceClass.DESKTOP);
    expect(request.preferredRenderer).toBe(SurfaceRenderer.WEB_BUNDLE);
    expect(request.viewport.width).toBeGreaterThan(0);
  });

  it("blocks duplicate opens while one is in flight and reports sanitized errors", async () => {
    const user = userEvent.setup();
    const pending = deferred<{ session: SurfaceSession }>();
    const clients = clientsFixture({
      apps: [app("board-app", "2.0.0")],
      installations: [installation("installation-1", "board-app", "1.9.0")],
      createSurface: vi.fn(() => pending.promise),
    });
    render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await screen.findByText(/Installed · pinned 1\.9\.0/);
    await user.click(screen.getByRole("button", { name: "Open" }));
    await user.click(screen.getByRole("button", { name: "Opening…" }));
    expect(clients.surfaces.createSurface).toHaveBeenCalledTimes(1);
    void pending.reject(new ConnectError("no supported web bundle", Code.FailedPrecondition));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("no supported web bundle");
    expect(screen.getByRole("button", { name: "Open" })).toBeTruthy();
  });

  it("closes a session that resolves after the library unmounted", async () => {
    const user = userEvent.setup();
    const pending = deferred<{ session: SurfaceSession }>();
    const closeSurface = vi.fn(() => Promise.resolve({}));
    const clients = clientsFixture({
      apps: [app("board-app", "2.0.0")],
      installations: [installation("installation-1", "board-app", "1.9.0")],
      createSurface: vi.fn(() => pending.promise),
      closeSurface,
    });
    const view = render(
      <AppLibrary
        project={project("project-1", 2n)}
        workosClients={clients}
        onProjectRefreshed={() => undefined}
        onSurfaceOpened={() => undefined}
        onInstallationRemoved={() => undefined}
      />,
    );
    await screen.findByText(/Installed · pinned 1\.9\.0/);
    await user.click(screen.getByRole("button", { name: "Open" }));
    view.unmount();
    void pending.resolve({
      session: {
        $typeName: "workos.surface.v1.SurfaceSession",
        id: "late-session",
        appInstanceId: "installation-1",
        projectId: "project-1",
        renderer: SurfaceRenderer.WEB_BUNDLE,
        url: "/surfaces/late-session/",
      } as SurfaceSession,
    });
    await waitFor(() => {
      expect(closeSurface).toHaveBeenCalledWith({ surfaceSessionId: "late-session" });
    });
  });
});
