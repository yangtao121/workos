import { openDesktopApp } from "./open-app.js";
import { expect, test, type Page } from "@playwright/test";

// The declarative surface gate (ADR-0016-era slice): a web-bundle app ships
// a versioned declarative document (`surface.json`, workos.declarative-
// surface/v1). WorkOS renders it with inert native components — no iframe,
// no scripts, no network — and safely degrades on schema violations.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

const declarativeDoc = {
  version: "workos.declarative-surface/v1",
  title: "Declarative Fixture",
  components: [
    { type: "markdown", text: "Quarterly review summary" },
    {
      type: "kv",
      rows: [
        { key: "Region", value: "eu-central" },
        { key: "Environment", value: "fixture" },
      ],
    },
    { type: "progress", value: 72 },
  ],
};

async function createBundle(
  page: Page,
  stamp: string,
  withDoc: boolean,
): Promise<{ artifact: { id: string; digest: string } }> {
  const files: { path: string; content: string }[] = [
    { path: "index.html", content: btoa("<!doctype html><title>Fallback</title>") },
    { path: "app.js", content: btoa("// declarative fixture bundle") },
  ];
  if (withDoc) {
    files.push({
      path: "surface.json",
      content: btoa(JSON.stringify(declarativeDoc)),
    });
  }
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-declarative-artifact-${stamp}-${String(withDoc)}`,
        artifact: { title: "Declarative E2E App" },
        webBundle: {
          entrypoint: "index.html",
          files,
        },
      },
    },
  );
  expect(artifactResponse.ok()).toBeTruthy();
  return (await artifactResponse.json()) as {
    artifact: { id: string; digest: string };
  };
}

async function registerApp(
  page: Page,
  stamp: string,
  appId: string,
  artifact: { id: string; digest: string },
) {
  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Declarative E2E App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: ${artifact.id}
  artifactDigest: ${artifact.digest}
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: []
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `e2e-declarative-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("renders declarative documents with inert native components", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `e2e-declarative-${stamp}`;

  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-declarative-project-${stamp}`,
        name: `Declarative E2E ${stamp}`,
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();
  const created = (await createResponse.json()) as { project: { id: string } };
  const projectId = created.project.id;

  const artifact = await createBundle(page, stamp, true);
  await registerApp(page, stamp, appId, artifact.artifact);

  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");

  await openDesktopApp(page, "app-library");
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Install" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });

  // The native declarative renderer ran: inert components, real document.
  const body = page.locator(".app-surface-body");
  await expect(body.locator(".declarative-markdown")).toHaveText("Quarterly review summary");
  await expect(body.locator(".declarative-kv dt").first()).toHaveText("Region");
  await expect(body.locator(".declarative-kv dd").nth(1)).toHaveText("fixture");
  await expect(body.locator(".declarative-progress")).toHaveAttribute("value", "72");
});
