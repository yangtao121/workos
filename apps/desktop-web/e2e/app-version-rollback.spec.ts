import { expect, test, type Page } from "@playwright/test";

// The app version transition / rollback gate (ADR-0012,
// docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md). Proves
// the owner-triggered version lifecycle over the REAL chain — two immutable
// web-bundle versions registered through the public API, installed through
// the App Library consent flow, surfaced in a sandboxed iframe, transitioned
// and rolled back through the Versions dialog (UI) and the public RPC
// (browser session) — including exact first-response replay and fail-closed
// conflicts. With WORKOS_CAPTURE_DIR set it also records the task's
// deterministic visual evidence.

const libraryTimeout = 30_000;
const captureDir = process.env.WORKOS_CAPTURE_DIR;

const bundlePage = (marker: string) =>
  `<!doctype html><title>Version E2E</title><div id="root">${marker}</div>`;

async function createBundle(page: Page, key: string, marker: string) {
  const response = await page.request.post("/workos.artifact.v1.ArtifactService/CreateArtifact", {
    data: {
      idempotencyKey: key,
      artifact: { title: `Version E2E ${marker}` },
      webBundle: {
        entrypoint: "index.html",
        files: [{ path: "index.html", content: btoa(bundlePage(marker)) }],
      },
    },
  });
  expect(response.ok()).toBeTruthy();
  const body = (await response.json()) as { artifact: { id: string; digest: string } };
  return body.artifact;
}

test("transitions and rolls back an installed app across surface generations", async ({ page }) => {
  test.setTimeout(240_000);
  const stamp = String(Date.now());
  const appId = `e2e-version-${stamp}`;
  const markerV1 = `version-e2e-v1-${stamp}`;
  const markerV2 = `version-e2e-v2-${stamp}`;

  const artifactV1 = await createBundle(page, `e2e-version-artifact-v1-${stamp}`, markerV1);
  const artifactV2 = await createBundle(page, `e2e-version-artifact-v2-${stamp}`, markerV2);

  const register = async (version: string, artifact: { id: string; digest: string }) => {
    const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Version E2E ${stamp}
version: ${version}
scope: user
runtime:
  type: web-bundle
  artifactId: ${artifact.id}
  artifactDigest: ${artifact.digest}
surfaces:
  - id: main
    renderer: web-bundle
    route: /
permissions: []
resources: {}
health: {}
maintainer: {}
`;
    const response = await page.request.post("/workos.app.v1.AppRegistryService/RegisterApp", {
      data: {
        idempotencyKey: `e2e-version-register-${version}-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    });
    expect(response.ok()).toBeTruthy();
  };
  // Register only v1 for now: the App Library installs the catalog's
  // current version, so the oldest pin must be the current at install time.
  await register("1.0.0", artifactV1);

  const projectName = `Version E2E ${stamp}`;
  const fetchProject = async (): Promise<{ id: string; revision: number }> => {
    const response = await page.request.post("/workos.project.v1.ProjectService/ListProjects", {
      data: { page: { pageSize: 100 } },
    });
    expect(response.ok()).toBeTruthy();
    const body = (await response.json()) as {
      projects: Array<{ id: string; name: string; revision: string }>;
    };
    const project = body.projects.find((candidate) => candidate.name === projectName);
    if (!project) throw new Error("version e2e project vanished");
    return { id: project.id, revision: Number(project.revision) };
  };
  const projectRevision = async (): Promise<number> => (await fetchProject()).revision;

  // Install v1 through the real consent flow.
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Version E2E ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText(`Version E2E ${stamp}`);
  await page.getByRole("button", { name: "App Library" }).click();
  const library = page.locator(".app-library");
  await expect(
    library.locator(".app-row", { hasText: appId }).getByText(/registry 1\.0\.0/),
  ).toBeVisible({
    timeout: libraryTimeout,
  });
  const row = library.locator(".app-row", { hasText: appId });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  await page.getByRole("dialog").waitFor({ timeout: libraryTimeout });
  await page.getByRole("button", { name: "Install without permissions" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // Only now register v2: the registry current moves, the installation's
  // pinned v1 does not.
  await register("1.1.0", artifactV2);

  // Open the v1 surface first: the v1 content is live in a window.
  await row.getByRole("button", { name: "Open" }).click();
  const firstSurface = page.locator("iframe");
  await expect(firstSurface).toBeVisible({ timeout: libraryTimeout });
  await expect(page.frameLocator("iframe").getByText(markerV1)).toBeVisible({
    timeout: libraryTimeout,
  });

  // Owner-visible transition via the Versions dialog (UI chain).
  await row.getByRole("button", { name: "Versions" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/pinned 1\.0\.0/)).toBeVisible({ timeout: libraryTimeout });
  await expect(dialog.locator(".version-row").first()).toContainText("install");
  await dialog.getByLabel("Target version").fill("1.1.0");
  await dialog.getByRole("button", { name: "Switch version" }).click();
  await expect(dialog.getByText(/Switched to 1\.1\.0/)).toBeVisible({ timeout: libraryTimeout });
  if (captureDir) {
    await page.screenshot({ path: `${captureDir}/app-library--version-switched--1440x900.png` });
  }
  await dialog.getByRole("button", { name: "Close" }).click();

  // The confirmed version change tore the stale v1 window down.
  await expect(page.locator("iframe")).toHaveCount(0, { timeout: libraryTimeout });

  // The new pinned descriptor serves the v2 surface.
  await row.getByRole("button", { name: "Open" }).click();
  const surface = page.locator("iframe");
  await expect(surface).toBeVisible({ timeout: libraryTimeout });
  await expect(page.frameLocator("iframe").getByText(markerV2)).toBeVisible({
    timeout: libraryTimeout,
  });

  // Owner-visible rollback through the dialog: the previous pinned snapshot
  // is restored server-side and the open v2 surface is torn down.
  await row.getByRole("button", { name: "Versions" }).click();
  const rollbackDialog = page.getByRole("dialog");
  await expect(rollbackDialog.getByRole("button", { name: "Roll back to 1.0.0" })).toBeVisible({
    timeout: libraryTimeout,
  });
  await rollbackDialog.getByRole("button", { name: "Roll back to 1.0.0" }).click();
  await expect(rollbackDialog.getByText(/Rolled back to 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  if (captureDir) {
    await page.screenshot({ path: `${captureDir}/app-library--rollback-complete--1440x900.png` });
  }
  await rollbackDialog.getByRole("button", { name: "Close" }).click();

  // The stale v2 surface window was closed by the confirmed rollback.
  await expect(page.locator("iframe")).toHaveCount(0, { timeout: libraryTimeout });

  // Server-side rollback through the browser session with a known key, then
  // an exact replay of the same canonical request.
  const revision = await projectRevision();
  const rollbackKey = `e2e-version-rollback-${stamp}`;
  const project = await fetchProject();
  const rollbackOnce = await page.request.post(
    "/workos.app.v1.AppInstallationService/RollbackAppVersion",
    {
      data: {
        idempotencyKey: rollbackKey,
        projectId: project.id,
        installationId: await installationId(page, project.id, appId),
        expectedProjectRevision: revision,
      },
    },
  );
  expect(rollbackOnce.ok()).toBeTruthy();
  const rollbackBody = (await rollbackOnce.json()) as {
    installation: { version: string };
    projectRevision: string;
    rolledBackToVersion: string;
  };
  // Already at 1.0.0, the most recent different snapshot is the v2 pin: this
  // rollback walks back to it — the server derives the target, whatever
  // direction that is.
  expect(rollbackBody.rolledBackToVersion).toBe("1.1.0");
  const replay = await page.request.post(
    "/workos.app.v1.AppInstallationService/RollbackAppVersion",
    {
      data: {
        idempotencyKey: rollbackKey,
        projectId: project.id,
        installationId: await installationId(page, project.id, appId),
        expectedProjectRevision: revision,
      },
    },
  );
  expect(replay.ok()).toBeTruthy();
  const replayBody = (await replay.json()) as {
    installation: { version: string };
    projectRevision: string;
    rolledBackToVersion: string;
  };
  expect(replayBody).toEqual(rollbackBody);

  // Fail closed: a stale expected revision is a stable conflict, an unknown
  // target version is sanitized NotFound — zero side effects.
  const stale = await page.request.post(
    "/workos.app.v1.AppInstallationService/TransitionAppVersion",
    {
      data: {
        idempotencyKey: `e2e-version-stale-${stamp}`,
        projectId: project.id,
        installationId: await installationId(page, project.id, appId),
        expectedProjectRevision: 1,
        version: "1.0.0",
      },
    },
  );
  expect(stale.status()).toBe(409);
  const unknown = await page.request.post(
    "/workos.app.v1.AppInstallationService/TransitionAppVersion",
    {
      data: {
        idempotencyKey: `e2e-version-unknown-${stamp}`,
        projectId: project.id,
        installationId: await installationId(page, project.id, appId),
        expectedProjectRevision: await projectRevision(),
        version: "9.9.9",
      },
    },
  );
  expect(unknown.status()).toBe(404);

  // The rolled-back-to v2 pin serves the v2 surface again after everything.
  await page.reload();
  await page.getByRole("button", { name: "App Library" }).click();
  const reloadedRow = page.locator(".app-library .app-row", { hasText: appId });
  await expect(reloadedRow.getByText(/Installed · pinned 1\.1\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await reloadedRow.getByRole("button", { name: "Open" }).click();
  await expect(page.frameLocator("iframe").getByText(markerV2)).toBeVisible({
    timeout: libraryTimeout,
  });
});

async function installationId(page: Page, projectId: string, appId: string): Promise<string> {
  const response = await page.request.post(
    "/workos.app.v1.AppInstallationService/ListInstalledApps",
    {
      data: { projectId, page: { pageSize: 100 } },
    },
  );
  expect(response.ok()).toBeTruthy();
  const body = (await response.json()) as { installations?: Array<{ id: string; appId: string }> };
  // proto3 JSON omits empty repeated fields.
  const installation = (body.installations ?? []).find((candidate) => candidate.appId === appId);
  if (!installation) {
    throw new Error(
      `version e2e installation vanished for ${appId} in ${projectId}: ${JSON.stringify(body).slice(0, 400)}`,
    );
  }
  return installation.id;
}
