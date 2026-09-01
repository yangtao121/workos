// @vitest-environment jsdom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import type { AppInstallation, Project } from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import { VersionDialog } from "./VersionDialog.js";
import type { WorkOSClients } from "@workos/agent-sdk";

const PROJECT_ID = "01990000-0000-7000-8000-000000000001";
const INSTALLATION_ID = "01990000-0000-7000-8000-0000000000a1";

function project(revision: bigint): Project {
  return {
    $typeName: "workos.project.v1.Project",
    id: PROJECT_ID,
    ownerUserId: "local-user",
    name: "Fixture Project",
    icon: "◈",
    workspaceRefs: [],
    installedAppIds: [INSTALLATION_ID],
    defaultAgentRole: "general",
    knowledgeCollectionId: "",
    artifactCollectionId: "",
    revision,
  };
}

function installation(version: string): AppInstallation {
  return {
    $typeName: "workos.app.v1.AppInstallation",
    id: INSTALLATION_ID,
    projectId: PROJECT_ID,
    appId: "board-app",
    version,
    manifestDigest: `sha256:${"a".repeat(64)}`,
    grantedPermissions: [],
    grantRevision: 1n,
  };
}

interface DialogClients {
  appInstallations: {
    listAppVersionHistory: ReturnType<typeof vi.fn>;
    transitionAppVersion: ReturnType<typeof vi.fn>;
    rollbackAppVersion: ReturnType<typeof vi.fn>;
    listInstalledApps: ReturnType<typeof vi.fn>;
  };
  projects: {
    getProject: ReturnType<typeof vi.fn>;
  };
}

function clientsFixture(overrides: {
  history?: Array<{ version: string; sequence: string; source: string }>;
  transitionFn?: () => Promise<unknown>;
  rollbackFn?: () => Promise<unknown>;
}): WorkOSClients & DialogClients {
  const listAppVersionHistory = vi.fn(() =>
    Promise.resolve({ snapshots: overrides.history ?? [] }),
  );
  const transitionAppVersion = vi.fn(() =>
    overrides.transitionFn
      ? overrides.transitionFn()
      : Promise.resolve({ installation: installation("1.2.0"), projectRevision: 6n }),
  );
  const rollbackAppVersion = vi.fn(() =>
    overrides.rollbackFn
      ? overrides.rollbackFn()
      : Promise.resolve({ installation: installation("1.0.0"), projectRevision: 6n }),
  );
  const listInstalledApps = vi.fn(() =>
    Promise.resolve({ installations: [], page: { nextPageToken: "" } }),
  );
  const getProject = vi.fn(() => Promise.resolve({ project: project(9n) }));
  return {
    appInstallations: {
      listAppVersionHistory,
      transitionAppVersion,
      rollbackAppVersion,
      listInstalledApps,
    },
    projects: { getProject },
  } as unknown as WorkOSClients & DialogClients;
}

function renderDialog(clients: WorkOSClients, version = "1.1.0") {
  const onInstallationSaved = vi.fn();
  const onVersionChanged = vi.fn();
  const view = render(
    <VersionDialog
      installation={installation(version)}
      onCancel={() => undefined}
      onFactsRefreshed={() => undefined}
      onInstallationSaved={onInstallationSaved}
      onVersionChanged={onVersionChanged}
      project={project(5n)}
      workosClients={clients}
    />,
  );
  return { view, onInstallationSaved, onVersionChanged };
}

afterEach(cleanup);

describe("VersionDialog", () => {
  it("lists the durable history and previews the rollback target", async () => {
    const clients = clientsFixture({
      history: [
        { version: "1.0.0", sequence: "1", source: "install" },
        { version: "1.1.0", sequence: "2", source: "transition" },
      ],
    });
    renderDialog(clients);
    expect(await screen.findByText(/pinned 1\.1\.0/)).toBeTruthy();
    expect(screen.getByText("1.0.0")).toBeTruthy();
    // The rollback button previews the exact previous version.
    expect(screen.getByRole("button", { name: "Roll back to 1.0.0" })).toBeTruthy();
  });

  it("hides the rollback action when no previous version exists", async () => {
    const clients = clientsFixture({
      history: [{ version: "1.0.0", sequence: "1", source: "install" }],
    });
    renderDialog(clients, "1.0.0");
    expect(await screen.findByRole("button", { name: "No previous version" })).toBeTruthy();
  });

  it("submits an explicit transition with a fresh key and the project revision", async () => {
    const user = userEvent.setup();
    const clients = clientsFixture({
      history: [
        { version: "1.0.0", sequence: "1", source: "install" },
        { version: "1.1.0", sequence: "2", source: "transition" },
      ],
    });
    const rendered = renderDialog(clients);
    await screen.findByText(/pinned 1\.1\.0/);
    await user.type(screen.getByLabelText("Target version"), "1.2.0");
    await user.click(screen.getByRole("button", { name: "Switch version" }));
    await waitFor(() => {
      expect(clients.appInstallations.transitionAppVersion).toHaveBeenCalledTimes(1);
    });
    const request = clients.appInstallations.transitionAppVersion.mock.calls[0]?.[0] as
      | { version: string; expectedProjectRevision: bigint; idempotencyKey: string }
      | undefined;
    expect(request?.version).toBe("1.2.0");
    expect(request?.expectedProjectRevision).toBe(5n);
    expect(request?.idempotencyKey).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByText(/Switched to 1\.2\.0/)).toBeTruthy();
    });
    expect(rendered.onInstallationSaved).toHaveBeenCalledWith(installation("1.2.0"), 6n);
    expect(rendered.onVersionChanged).toHaveBeenCalledWith(INSTALLATION_ID);
  });

  it("maps an incompatible target to the permissions-need-review copy", async () => {
    const user = userEvent.setup();
    const clients = clientsFixture({
      // The rejection is created per call so the fixture never holds an
      // unconsumed rejected promise.
      transitionFn: () =>
        Promise.reject(
          new ConnectError(
            "current permissions are not compatible with the target version; review permissions first",
            Code.FailedPrecondition,
          ),
        ),
    });
    renderDialog(clients);
    await screen.findByText(/pinned 1\.1\.0/);
    await user.type(screen.getByLabelText("Target version"), "2.0.0");
    await user.click(screen.getByRole("button", { name: "Switch version" }));
    await waitFor(() => {
      expect(screen.getByText(/Permissions need review/)).toBeTruthy();
    });
  });
});
