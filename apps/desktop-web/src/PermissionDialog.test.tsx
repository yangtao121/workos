// @vitest-environment jsdom

import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  AppScope,
  type AppInstallation,
  type GetAppResponse,
  type Project,
  type SetAppGrantsResponse,
  type WorkOSApp,
} from "@workos/protocol";
import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PermissionDialog, type PermissionFacts } from "./PermissionDialog.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(cleanup);

function registryApp(
  id: string,
  version: string,
  permissions: string[],
  digest = `sha256:${id.padEnd(64, "0").slice(0, 64)}`,
): WorkOSApp {
  return {
    $typeName: "workos.app.v1.WorkOSApp",
    id,
    name: id.replace(/-/g, " "),
    version,
    scope: AppScope.USER,
    permissions,
    manifestDigest: digest,
  };
}

function installationFixture(overrides: Partial<AppInstallation> = {}): AppInstallation {
  const appId = "board-app";
  return {
    $typeName: "workos.app.v1.AppInstallation",
    id: "installation-1",
    projectId: "project-1",
    appId,
    version: "1.9.0",
    manifestDigest: `sha256:${appId.padEnd(64, "0").slice(0, 64)}`,
    installedAt: { $typeName: "google.protobuf.Timestamp", seconds: 1787000000n, nanos: 0 },
    uninstalledAt: undefined,
    grantedPermissions: ["agent.task.run"],
    grantRevision: 2n,
    ...overrides,
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

function getAppResponse(app: WorkOSApp): GetAppResponse {
  return { $typeName: "workos.app.v1.GetAppResponse", app };
}

function setAppGrantsResponse(
  installation: AppInstallation,
  revision: bigint,
): SetAppGrantsResponse {
  return {
    $typeName: "workos.app.v1.SetAppGrantsResponse",
    installation,
    projectRevision: revision,
  };
}

interface DialogFixture {
  getApp?: ReturnType<typeof vi.fn>;
  setAppGrants?: ReturnType<typeof vi.fn>;
}

function dialogClients(fixture: DialogFixture) {
  const pinned = registryApp("board-app", "1.9.0", ["agent.task.run", "agent.event.watch"]);
  return {
    appRegistry: {
      getApp: fixture.getApp ?? vi.fn(() => Promise.resolve(getAppResponse(pinned))),
    },
    appInstallations: {
      setAppGrants:
        fixture.setAppGrants ??
        vi.fn(() =>
          Promise.resolve(
            setAppGrantsResponse(
              installationFixture({ grantedPermissions: [], grantRevision: 3n }),
              6n,
            ),
          ),
        ),
    },
  } as unknown as Pick<WorkOSClients, "appRegistry" | "appInstallations">;
}

interface RenderOptions {
  installation?: AppInstallation;
  projectRevision?: bigint;
  clients?: Pick<WorkOSClients, "appRegistry" | "appInstallations">;
  facts?: PermissionFacts;
}

function renderDialog({
  installation = installationFixture(),
  projectRevision = 5n,
  clients,
  facts,
}: RenderOptions = {}) {
  const activeProject = project("project-1", projectRevision);
  const activeClients = clients ?? dialogClients({});
  const readFacts = vi.fn(
    () =>
      new Promise<PermissionFacts>((resolve) => {
        resolve(facts ?? { project: activeProject, installations: [installation] });
      }),
  );
  const onFactsRefreshed = vi.fn();
  const onGrantsApplied = vi.fn();
  const onCancel = vi.fn();
  const view = render(
    <PermissionDialog
      installation={installation}
      project={activeProject}
      workosClients={activeClients}
      readFacts={readFacts}
      onFactsRefreshed={onFactsRefreshed}
      onGrantsApplied={onGrantsApplied}
      onCancel={onCancel}
    />,
  );
  return { view, readFacts, onFactsRefreshed, onGrantsApplied, onCancel, clients: activeClients };
}

function saveButton(): HTMLButtonElement {
  return screen.getByRole<HTMLButtonElement>("button", {
    name: /Save permissions|Revoke all permissions|Saving…/,
  });
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

describe("PermissionDialog requested set", () => {
  it("resolves the exact pinned version, never the catalog current one, and seeds from the current grant", async () => {
    const user = userEvent.setup();
    // The catalog's current version is 2.0.0 with a different permission set;
    // the installation pins 1.9.0. Only 1.9.0 may be the edit basis.
    const getApp = vi.fn(() =>
      Promise.resolve(
        getAppResponse(registryApp("board-app", "1.9.0", ["agent.task.run", "agent.event.watch"])),
      ),
    );
    const clients = dialogClients({ getApp });
    renderDialog({
      clients,
      installation: installationFixture({ grantedPermissions: ["agent.task.run"] }),
    });

    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(getApp).toHaveBeenCalledWith({ appId: "board-app", version: "1.9.0" });
    expect(screen.getByText(/pinned version 1\.9\.0/)).toBeTruthy();
    expect(screen.getByText(/Current grant revision 2/)).toBeTruthy();
    // Checkboxes start from the current grant — never from a default-all.
    expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "agent.task.run" }).checked).toBe(
      true,
    );
    expect(
      screen.getByRole<HTMLInputElement>("checkbox", { name: "agent.event.watch" }).checked,
    ).toBe(false);
    expect(screen.getByText("No changes to save.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
    // Nothing is submittable without an explicit user change.
    await user.click(saveButton());
    expect(clients.appInstallations.setAppGrants).not.toHaveBeenCalled();
  });

  it("fails closed on a manifest digest mismatch without any submittable checkbox", async () => {
    const setAppGrants = vi.fn();
    const drift = registryApp("board-app", "1.9.0", ["agent.task.run"], `sha256:${"9".repeat(64)}`);
    const clients = dialogClients({
      getApp: vi.fn(() => Promise.resolve(getAppResponse(drift))),
      setAppGrants,
    });
    renderDialog({ clients });

    expect(
      await screen.findByText(
        "The app version could not be verified. Permissions cannot be edited.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Save permissions|Revoke all permissions/ }),
    ).toBeNull();
    expect(setAppGrants).not.toHaveBeenCalled();
  });

  it("fails closed when the registry answers with a different app id", async () => {
    const clients = dialogClients({
      getApp: vi.fn(() =>
        Promise.resolve(getAppResponse(registryApp("other-app", "1.9.0", ["agent.task.run"]))),
      ),
    });
    renderDialog({ clients });

    expect(
      await screen.findByText(
        "The app version could not be verified. Permissions cannot be edited.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("offers a retry when the exact pinned version cannot be loaded", async () => {
    const user = userEvent.setup();
    const pinned = registryApp("board-app", "1.9.0", ["agent.task.run"]);
    let failing = true;
    const getApp = vi.fn(() => {
      if (failing) return Promise.reject(new ConnectError("offline", Code.Unavailable));
      return Promise.resolve(getAppResponse(pinned));
    });
    const clients = dialogClients({ getApp });
    renderDialog({ clients });

    expect(await screen.findByText("The pinned app version could not be loaded.")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
    failing = false;
    await user.click(screen.getByRole("button", { name: "Retry permissions" }));
    expect(await screen.findByRole("checkbox", { name: "agent.task.run" })).toBeTruthy();
    expect(getApp).toHaveBeenCalledTimes(2);
  });
});

describe("PermissionDialog replacement flow", () => {
  it("shows the added/removed diff and only enables Save for a real change", async () => {
    const user = userEvent.setup();
    renderDialog({ installation: installationFixture({ grantedPermissions: ["agent.task.run"] }) });

    await screen.findByRole("checkbox", { name: "agent.task.run" });
    expect(screen.getByText("No changes to save.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);

    await user.click(screen.getByRole("checkbox", { name: "agent.event.watch" }));
    expect(screen.getByText("Adding agent.event.watch")).toBeTruthy();
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "Save permissions" }).disabled,
    ).toBe(false);

    await user.click(screen.getByRole("checkbox", { name: "agent.task.run" }));
    expect(screen.getByText("Removing agent.task.run")).toBeTruthy();
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "Save permissions" }).disabled,
    ).toBe(false);

    // Back to the current grant: a same-set submit must be impossible.
    await user.click(screen.getByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("checkbox", { name: "agent.event.watch" }));
    expect(screen.getByText("No changes to save.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("labels an empty target set as revoking everything and sends the full replacement", async () => {
    const user = userEvent.setup();
    const setAppGrants = vi.fn(() =>
      Promise.resolve(
        setAppGrantsResponse(
          installationFixture({ grantedPermissions: [], grantRevision: 3n }),
          6n,
        ),
      ),
    );
    const clients = dialogClients({ setAppGrants });
    const facts: PermissionFacts = {
      project: project("project-1", 6n),
      installations: [installationFixture({ grantedPermissions: [], grantRevision: 3n })],
    };
    renderDialog({
      clients,
      facts,
      installation: installationFixture({ grantedPermissions: ["agent.task.run"] }),
      projectRevision: 5n,
    });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    expect(screen.getByText("Saving with nothing selected revokes every permission.")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));

    expect(
      await screen.findByText(
        "Permissions saved. Reopen the app for the new permissions to take effect.",
      ),
    ).toBeTruthy();
    expect(setAppGrants).toHaveBeenCalledWith({
      idempotencyKey: expect.any(String) as string,
      projectId: "project-1",
      installationId: "installation-1",
      expectedProjectRevision: 5n,
      grantedPermissions: [],
    });
  });

  it("sends an explicit Save only; Cancel never issues a request", async () => {
    const user = userEvent.setup();
    const setAppGrants = vi.fn();
    const clients = dialogClients({ setAppGrants });
    const { onCancel } = renderDialog({ clients });

    await user.click(await screen.findByRole("checkbox", { name: "agent.event.watch" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(setAppGrants).not.toHaveBeenCalled();
  });

  it("blocks a duplicate Save while one request is in flight", async () => {
    const user = userEvent.setup();
    const pending = deferred<SetAppGrantsResponse>();
    const setAppGrants = vi.fn(() => pending.promise);
    const clients = dialogClients({ setAppGrants });
    renderDialog({ clients });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
    const saving = screen.getByRole<HTMLButtonElement>("button", { name: "Saving…" });
    expect(saving.disabled).toBe(true);
    await user.click(saving);
    expect(setAppGrants).toHaveBeenCalledTimes(1);

    await act(async () => {
      await pending.resolve(
        setAppGrantsResponse(
          installationFixture({ grantedPermissions: [], grantRevision: 3n }),
          6n,
        ),
      );
    });
    expect(
      await screen.findByText(
        "Permissions saved. Reopen the app for the new permissions to take effect.",
      ),
    ).toBeTruthy();
  });

  it("reloads facts and demands reconfirmation after a revision conflict instead of replaying", async () => {
    const user = userEvent.setup();
    const setAppGrants = vi.fn(() =>
      Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
    );
    const getApp = vi.fn(() =>
      Promise.resolve(
        getAppResponse(registryApp("board-app", "1.9.0", ["agent.task.run", "agent.event.watch"])),
      ),
    );
    const clients = dialogClients({ getApp, setAppGrants });
    // Someone else changed the grant in the meantime: the fresh server truth
    // is watch-only at revision 3 under project revision 7.
    const freshInstallation = installationFixture({
      grantedPermissions: ["agent.event.watch"],
      grantRevision: 3n,
    });
    const freshProject = project("project-1", 7n);
    const { onFactsRefreshed } = renderDialog({
      clients,
      facts: { project: freshProject, installations: [freshInstallation] },
      installation: installationFixture({ grantedPermissions: ["agent.task.run"] }),
    });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));

    expect(
      await screen.findByText(
        "Project settings changed elsewhere. Review the latest permissions and save again.",
      ),
    ).toBeTruthy();
    expect(onFactsRefreshed).toHaveBeenCalledWith({
      project: freshProject,
      installations: [freshInstallation],
    });
    // The dialog reseeded from the fresh grant: watch granted, run not.
    expect(
      screen.getByRole<HTMLInputElement>("checkbox", { name: "agent.event.watch" }).checked,
    ).toBe(true);
    expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "agent.task.run" }).checked).toBe(
      false,
    );
    // The old selection was discarded, not replayed, and no second request
    // went out on its own.
    expect(setAppGrants).toHaveBeenCalledTimes(1);
    expect(screen.getByText("No changes to save.")).toBeTruthy();
    expect(saveButton().disabled).toBe(true);
  });

  it("reports the installation as gone when a conflict reveals it was uninstalled", async () => {
    const user = userEvent.setup();
    const clients = dialogClients({
      setAppGrants: vi.fn(() =>
        Promise.reject(new ConnectError("project revision conflict", Code.Aborted)),
      ),
    });
    renderDialog({
      clients,
      facts: { project: project("project-1", 7n), installations: [] },
    });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
    expect(await screen.findByText("The app is no longer installed.")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("shows a fixed failure and stays editable on an ordinary save error", async () => {
    const user = userEvent.setup();
    const clients = dialogClients({
      setAppGrants: vi.fn(() =>
        Promise.reject(new ConnectError("subset violation", Code.PermissionDenied)),
      ),
    });
    renderDialog({ clients });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
    expect(
      await screen.findByText("The permission change was rejected. Reload and try again."),
    ).toBeTruthy();
    // The dialog remains usable for another explicit attempt.
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "Revoke all permissions" }).disabled,
    ).toBe(false);
  });
});

describe("PermissionDialog success and liveness", () => {
  it("adopts the server response plus re-read facts and tears down exactly the target installation", async () => {
    const user = userEvent.setup();
    const applied = installationFixture({ grantedPermissions: [], grantRevision: 3n });
    const setAppGrants = vi.fn(() => Promise.resolve(setAppGrantsResponse(applied, 6n)));
    const clients = dialogClients({ setAppGrants });
    const freshFacts: PermissionFacts = {
      project: project("project-1", 6n),
      installations: [applied],
    };
    const { onFactsRefreshed, onGrantsApplied, readFacts } = renderDialog({
      clients,
      facts: freshFacts,
    });

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));

    expect(
      await screen.findByText(
        "Permissions saved. Reopen the app for the new permissions to take effect.",
      ),
    ).toBeTruthy();
    expect(onGrantsApplied).toHaveBeenCalledTimes(1);
    expect(onGrantsApplied).toHaveBeenCalledWith("installation-1");
    expect(readFacts).toHaveBeenCalledTimes(1);
    expect(onFactsRefreshed).toHaveBeenCalledWith(freshFacts);
    // Each explicit Save mints its own idempotency key.
    const request = (
      setAppGrants.mock.calls as unknown as Array<[{ idempotencyKey: string }]>
    )[0]?.[0];
    expect(request?.idempotencyKey).toEqual(expect.any(String));
  });

  it("still reports success from the Set response when the post-save re-read fails", async () => {
    const user = userEvent.setup();
    const applied = installationFixture({ grantedPermissions: [], grantRevision: 3n });
    const clients = dialogClients({
      setAppGrants: vi.fn(() => Promise.resolve(setAppGrantsResponse(applied, 6n))),
    });
    const activeProject = project("project-1", 5n);
    const readFacts = vi.fn(() => Promise.reject(new ConnectError("offline", Code.Unavailable)));
    const onGrantsApplied = vi.fn();
    render(
      <PermissionDialog
        installation={installationFixture()}
        project={activeProject}
        workosClients={clients}
        readFacts={readFacts}
        onFactsRefreshed={vi.fn()}
        onGrantsApplied={onGrantsApplied}
        onCancel={vi.fn()}
      />,
    );

    await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
    await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
    expect(
      await screen.findByText(
        "Permissions saved. Reopen the app for the new permissions to take effect.",
      ),
    ).toBeTruthy();
    expect(onGrantsApplied).toHaveBeenCalledWith("installation-1");
  });

  it("ignores a late success that settles after the dialog unmounted", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const user = userEvent.setup();
      const pending = deferred<SetAppGrantsResponse>();
      const clients = dialogClients({ setAppGrants: vi.fn(() => pending.promise) });
      const { view, onGrantsApplied, onFactsRefreshed } = renderDialog({ clients });

      await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
      await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
      view.unmount();
      await act(async () => {
        await pending.resolve(
          setAppGrantsResponse(
            installationFixture({ grantedPermissions: [], grantRevision: 3n }),
            6n,
          ),
        );
      });
      expect(onGrantsApplied).not.toHaveBeenCalled();
      expect(onFactsRefreshed).not.toHaveBeenCalled();
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
    }
  });

  it("ignores a late conflict that settles after the dialog unmounted", async () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const user = userEvent.setup();
      const pending = deferred<never>();
      const clients = dialogClients({ setAppGrants: vi.fn(() => pending.promise) });
      const { view, onFactsRefreshed, readFacts } = renderDialog({ clients });

      await user.click(await screen.findByRole("checkbox", { name: "agent.task.run" }));
      await user.click(screen.getByRole("button", { name: "Revoke all permissions" }));
      view.unmount();
      await act(async () => {
        await pending
          .reject(new ConnectError("project revision conflict", Code.Aborted))
          .catch(() => undefined);
      });
      expect(readFacts).not.toHaveBeenCalled();
      expect(onFactsRefreshed).not.toHaveBeenCalled();
      expect(consoleError).not.toHaveBeenCalled();
    } finally {
      consoleError.mockRestore();
    }
  });
});
