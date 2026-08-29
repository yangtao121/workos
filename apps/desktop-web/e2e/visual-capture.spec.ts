import { expect, test } from "@playwright/test";

// Visual capture for the supervised web-service workload slice
// (docs/tasks/20260829-supervised-web-service-workload.md). Run explicitly:
//   pnpm exec playwright test visual-capture.spec.ts
// with WORKOS_CAPTURE_DIR pointing at the task's docs/ui capture folder.
// It seeds a strict digest-pinned container manifest through the public API
// and captures the honest states this deployment can produce without a
// rootless Podman host: the bounded open-failure copy and the System Monitor
// window.

const libraryTimeout = 30_000;

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "/captures";

const manifest = (appId: string, stamp: string) => `apiVersion: workos.app/v1
id: ${appId}
name: Supervised E2E ${stamp}
version: 1.0.0
scope: user
runtime:
  type: container
  image: localhost/workos-web-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-web-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: []
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
maintainer: {}
`;

test("captures the web-service states", async ({ page }) => {
  // Explicit capture tooling: never part of the standing e2e gate (it needs
  // WORKOS_CAPTURE_DIR pointing at the task's docs/ui folder).
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(180_000);
  const stamp = String(Date.now());
  const appId = `e2e-supervised-${stamp}`;

  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `e2e-supervised-register-${stamp}`,
        manifestYaml: Buffer.from(manifest(appId, stamp)).toString("base64"),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Supervised E2E ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText(`Supervised E2E ${stamp}`);

  await page.getByRole("button", { name: "App Library" }).click();
  const library = page.locator(".app-library");
  await expect(library.getByText(`${appId} · registry 1.0.0`)).toBeVisible({
    timeout: libraryTimeout,
  });

  const row = library.locator(".app-row", { hasText: appId });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  await page.getByRole("dialog").waitFor({ timeout: libraryTimeout });
  await page.getByRole("button", { name: "Install without permissions" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // Open the container app: this deployment reports the verified runner
  // unavailable, so the UI must show the bounded honest failure — never a
  // fake ready surface.
  await row.getByRole("button", { name: "Open" }).click();
  const alert = page.getByRole("alert");
  await expect(alert).toContainText(/no supported surface renderer|temporarily unavailable/, {
    timeout: libraryTimeout,
  });
  await alert.scrollIntoViewIfNeeded();
  await page.screenshot({
    path: `${captureDir}/app-library--web-service-start-unavailable--1440x900.png`,
  });

  // System Monitor: a normal, non-permanent window fed by the reliability
  // upstream (running in this stack) with a fixed empty state.
  await page.getByTestId("open-system-monitor").click();
  await expect(page.getByText(/No incidents recorded for this project\./)).toBeVisible({
    timeout: libraryTimeout,
  });
  await page.screenshot({
    path: `${captureDir}/system-monitor--no-incidents--1440x900.png`,
  });
});
