import { expect, test, type Page } from "@playwright/test";

// The remote-native status surface gate (W3.5 hosting slice): a remote
// native app is installed and opened as an opaque-origin surface window
// rendering its bounded status page through the supervised web surface path.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

async function createBundle(page: Page, stamp: string) {
  const html = `<!doctype html><html><head><meta charset="utf-8"><title>Native Runner</title></head>
<body id="native-runner">
<h1 id="native-title">Native runner</h1>
<p id="native-state">status: OK</p>
</body></html>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-native-artifact-${stamp}`,
        artifact: { title: "Native Runner Fixture" },
        webBundle: {
          entrypoint: "index.html",
          files: [{ path: "index.html", content: btoa(html) }],
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
name: Native Status
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
        idempotencyKey: `e2e-native-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("remote-native status surface renders through the supervised path", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-native-${stamp}`;

  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-native-project-${stamp}`,
        name: `Native E2E ${stamp}`,
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();

  const artifact = await createBundle(page, stamp);
  await registerApp(page, stamp, appId, artifact.artifact);

  await page.goto("/");
  await page.getByRole("button", { name: "App Library" }).click();
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

  const nativeBody = page
    .frameLocator(".app-surface-frame")
    .locator("#native-runner");
  await expect(nativeBody).toBeVisible({ timeout: libraryTimeout });
  await expect(
    page.frameLocator(".app-surface-frame").locator("#native-title"),
  ).toHaveText("Native runner");
  await expect(
    page.frameLocator(".app-surface-frame").locator("#native-state"),
  ).toHaveText("status: OK");

  // Owner-side uninstall tears the surface window down (session revalidation
  // closes stale windows server-side).
  await row.getByRole("button", { name: "Remove", exact: true }).click();
  const confirm = page.getByRole("dialog");
  if (await confirm.isVisible()) {
    await confirm.getByRole("button", { name: /Remove|Uninstall/ }).click();
  }
  await expect(frame).toBeHidden({ timeout: 30_000 });
});
