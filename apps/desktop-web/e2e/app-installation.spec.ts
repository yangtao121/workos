import { expect, test } from "@playwright/test";

// The persistent acceptance volume accumulates registered apps across runs,
// so the catalog walk in the App Library spans many pages; give the library
// time to finish loading before asserting on specific rows.
const libraryTimeout = 30_000;

const manifest = (appId: string, version: string) => `apiVersion: workos.app/v1
id: ${appId}
name: ${appId.replace(/-/g, " ")}
version: ${version}
scope: user
runtime:
  type: container
  command: ["./serve"]
surfaces:
  - id: main
    renderer: web-bundle
permissions: [artifact.read]
resources: {}
health: {}
maintainer: {}
`;

// The user path proven here runs through the real Gateway → Core →
// PostgreSQL chain only: register a synthetic app, install it from the App
// Library, reload the page and see it persist, then remove it and see it
// disappear after another reload. No direct database writes impersonate the
// user chain.
test.setTimeout(120_000);

test("installs a registered app into a project and persists it across reloads", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-install-${stamp}`;

  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E Install ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E Install");

  // Register two synthetic apps directly through the public registry API
  // before the library loads, so the catalog walk includes them: one is the
  // install target, the other stays foreign to this project's installed set.
  const register = async (id: string, version: string) => {
    const response = await page.request.post("/workos.app.v1.AppRegistryService/RegisterApp", {
      data: {
        idempotencyKey: `e2e-register-${id}-${version}-${stamp}`,
        manifestYaml: btoa(manifest(id, version)),
      },
    });
    expect(response.ok()).toBeTruthy();
  };
  await register(appId, "1.0.0");
  await register(`${appId}-other`, "1.0.0");

  await page.getByRole("button", { name: "App Library" }).click();
  const library = page.locator(".app-library");

  await expect(library.getByText(`${appId} · registry 1.0.0`)).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(library.getByText(`${appId}-other · registry 1.0.0`)).toBeVisible();

  // Target the specific app row: the owner's catalog holds many historical
  // apps, so "the first Install button" is not this test's app.
  const row = library.locator(".app-row", { hasText: appId }).filter({
    hasNotText: `${appId}-other`,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(row.getByRole("button", { name: "Remove" })).toBeVisible();

  // The project projection reflects the server-confirmed installation.
  await expect(page.locator(".project-card.active")).toContainText("revision 2");

  // Reload: the installation list and pinned version are served from
  // durable state, not from browser memory.
  // The desktop restores the last active project across a reload, so the
  // library opens straight onto this test's project.
  await page.reload();
  await expect(page.locator(".project-card.active")).toContainText(`E2E Install ${stamp}`);
  await page.getByRole("button", { name: "App Library" }).click();
  await expect(
    page
      .locator(".app-library .app-row", { hasText: appId })
      .filter({ hasNotText: `${appId}-other` })
      .getByText(/Installed · pinned 1\.0\.0/),
  ).toBeVisible({ timeout: libraryTimeout });

  // Uninstall through the same boundary, then reload again: the app is gone
  // from the installed set but remains in the owner's registry catalog.
  const reloadedRow = page.locator(".app-library .app-row", { hasText: appId }).filter({
    hasNotText: `${appId}-other`,
  });
  await reloadedRow.getByRole("button", { name: "Remove" }).click();
  await expect(reloadedRow.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(page.locator(".project-card.active")).toContainText("revision 3");

  await page.reload();
  await expect(page.locator(".project-card.active")).toContainText(`E2E Install ${stamp}`);
  await page.getByRole("button", { name: "App Library" }).click();
  const finalRow = page.locator(".app-library .app-row", { hasText: appId }).filter({
    hasNotText: `${appId}-other`,
  });
  await expect(finalRow.getByText(`${appId} · registry 1.0.0`)).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(finalRow.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
});
