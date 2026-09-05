import { expect, test, type Page } from "@playwright/test";

// The browser surface gate (W3.4 slice): a "Browser" app whose bundle is the
// remote-browser fixture page is installed and opened as an opaque-origin
// surface window. The page exercises the browser-worker contract — bounded
// tabs, address readout, content pane — through the same supervised web
// surface path, and the session revalidation keeps stale windows closed.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

async function createBundle(page: Page, stamp: string) {
  const html = `<!doctype html><html><head><meta charset="utf-8"><title>Browser Fixture</title></head>
<body id="browser-fixture">
<nav id="tabs" aria-label="Browser tabs"><span class="tab active">start</span><span class="tab">docs</span><span class="tab">review</span></nav>
<div id="address">fixture://start</div>
<main id="content">Browser fixture start page</main>
</body></html>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-browser-artifact-${stamp}`,
        artifact: { title: "Browser Fixture" },
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
name: Browser Fixture
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
        idempotencyKey: `e2e-browser-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("browser surface window renders the fixture browser chrome", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-browser-${stamp}`;

  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-browser-project-${stamp}`,
        name: `Browser E2E ${stamp}`,
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

  const browserBody = page
    .frameLocator(".app-surface-frame")
    .locator("#browser-fixture");
  await expect(browserBody).toBeVisible({ timeout: libraryTimeout });
  await expect(frame.contentFrame().locator("#tabs .tab.active")).toHaveText(
    "start",
  );
  await expect(frame.contentFrame().locator("#content")).toContainText(
    "Browser fixture start page",
  );

  // Session revalidation closes stale surfaces: after the owner closes the
  // window the surface session is closed best-effort server-side.
  await row.getByRole("button", { name: "Remove", exact: true }).click();
  const confirm = page.getByRole("dialog");
  if (await confirm.isVisible()) {
    await confirm.getByRole("button", { name: /Remove|Uninstall/ }).click();
  }
  await expect(frame).toBeHidden({ timeout: 30_000 });
});
