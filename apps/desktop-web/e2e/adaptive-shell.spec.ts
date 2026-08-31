import { expect, test, type Page } from "@playwright/test";

// The adaptive shell gate (docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md).
// Proves the four layout modes against the REAL stack — Gateway session,
// Core projects, App Registry + Installation, the fake-harness Agent chain,
// and the Reliability-backed System Monitor — at the three canonical
// viewports plus an injected fold-segment fixture. The expanded desktop is
// re-asserted so the adaptive slice cannot silently regress it.

const libraryTimeout = 30_000;

const manifest = (appId: string, stamp: string) => `apiVersion: workos.app/v1
id: ${appId}
name: Adaptive E2E ${stamp}
version: 1.0.0
scope: user
runtime:
  type: container
  image: localhost/workos-e2e-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-e2e-fixture", "serve"]
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

async function createProjectViaSheet(page: Page, name: string) {
  await page.getByTestId("nav-projects").click();
  const dialog = page.getByRole("dialog", { name: "Projects" });
  await dialog.waitFor({ timeout: libraryTimeout });
  await dialog.getByLabel("Project name").fill(name);
  await dialog.getByRole("button", { name: "Create space" }).click();
  // Creating selects the new project; close the sheet to continue.
  await dialog.getByRole("button", { name: "Close" }).click();
}

test.describe("adaptive compact (390x844)", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("drives project, app library, agent, and monitor from the bottom nav", async ({ page }) => {
    test.setTimeout(180_000);
    const stamp = String(Date.now());
    const appId = `e2e-adaptive-${stamp}`;
    const register = await page.request.post("/workos.app.v1.AppRegistryService/RegisterApp", {
      data: {
        idempotencyKey: `e2e-adaptive-register-${stamp}`,
        manifestYaml: Buffer.from(manifest(appId, stamp)).toString("base64"),
      },
    });
    expect(register.ok()).toBeTruthy();

    await page.goto("/");
    await expect(page.getByTestId("adaptive-bottom-nav")).toBeVisible({
      timeout: libraryTimeout,
    });
    await createProjectViaSheet(page, `Adaptive Compact ${stamp}`);

    // App Library overlay: the registered app is listed and installs with
    // the explicit consent flow, entirely inside the phone shell.
    await page.getByTestId("nav-apps").click();
    const overlay = page.getByTestId("adaptive-apps-overlay");
    await expect(overlay.getByText(new RegExp(`${appId} · registry 1\\.0\\.0`))).toBeVisible({
      timeout: libraryTimeout,
    });
    await overlay
      .locator(".app-row", { hasText: appId })
      .getByRole("button", { name: "Install", exact: true })
      .click();
    await page.getByRole("dialog").waitFor({ timeout: libraryTimeout });
    await page.getByRole("button", { name: "Install without permissions" }).click();
    await expect(overlay.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
      timeout: libraryTimeout,
    });
    await overlay.getByRole("button", { name: "Close" }).click();

    // Agent Center: a real project task runs through the fake harness.
    await page.getByTestId("nav-agent").click();
    await page.getByLabel("Agent goal").fill("Adaptive compact task");
    await page.getByRole("button", { name: "Run task" }).click();
    await expect(page.getByText(/completed by fake harness/)).toBeVisible({
      timeout: libraryTimeout,
    });

    // System Monitor is reachable and fed by the reliability upstream.
    await page.getByTestId("nav-monitor").click();
    await expect(page.getByText(/No incidents recorded for this project\./)).toBeVisible({
      timeout: libraryTimeout,
    });

    // The device-local layout record survives a reload in the compact shell.
    await page.reload();
    await expect(page.getByTestId("adaptive-bottom-nav")).toBeVisible({
      timeout: libraryTimeout,
    });
  });
});

test.describe("adaptive medium (820x1180)", () => {
  test.use({ viewport: { width: 820, height: 1180 } });

  test("uses an Agent slide-over and an explicit dock instead of permanent panes", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const stamp = String(Date.now());
    await page.goto("/");
    await expect(page.getByTestId("open-agent-slideover")).toBeVisible({
      timeout: libraryTimeout,
    });
    await expect(page.getByTestId("adaptive-bottom-nav")).toBeHidden();
    await createProjectViaSheet(page, `Adaptive Medium ${stamp}`);

    await page.getByTestId("open-agent-slideover").click();
    const slideOver = page.getByTestId("agent-slideover");
    await expect(slideOver).toBeVisible();
    await slideOver.getByLabel("Agent goal").fill("Adaptive medium task");
    await slideOver.getByRole("button", { name: "Run task" }).click();
    await expect(page.getByText(/completed by fake harness/)).toBeVisible({
      timeout: libraryTimeout,
    });
    await page.keyboard.press("Escape");
    await expect(slideOver).toBeHidden();

    // The dock is hidden until explicitly revealed, and hides again on use.
    await page.getByTestId("toggle-dock").click();
    const dock = page.getByTestId("adaptive-dock");
    await expect(dock).toBeVisible();
    await dock.getByRole("button", { name: "System Monitor" }).click();
    await expect(dock).toBeHidden();
    await expect(page.getByText(/No incidents recorded for this project\./)).toBeVisible({
      timeout: libraryTimeout,
    });
  });
});

test.describe("adaptive fold-separated (1280x800)", () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test("shows dual panes only with two window segments and degrades safely without", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const stamp = String(Date.now());
    // Two side-by-side segments with a 16px hinge, injected in place of the
    // flag-gated Window Segments Management API.
    await page.addInitScript(() => {
      Object.defineProperty(window, "getWindowSegments", {
        configurable: true,
        value: () => [
          { x: 0, y: 0, width: 632, height: 800, top: 0, left: 0, right: 632, bottom: 800 },
          { x: 648, y: 0, width: 632, height: 800, top: 0, left: 648, right: 1280, bottom: 800 },
        ],
      });
    });
    await page.goto("/");
    await expect(page.getByTestId("fold-pane-main")).toBeVisible({ timeout: libraryTimeout });
    // The hinge gap never carries content, and the second pane exists.
    await expect(page.getByTestId("fold-pane-secondary")).toBeVisible();
    await expect(page.locator(".fold-hinge")).toHaveCount(1);
    await createProjectViaSheet(page, `Adaptive Fold ${stamp}`);
    // The user can drop to a single pane explicitly.
    await page.getByTestId("toggle-panes").click();
    await expect(page.getByTestId("fold-pane-secondary")).toBeHidden();

    // Without segments the shell degrades by width — never a forced split.
    // At desktop width a segment-less foldable is expanded; at tablet width
    // it is medium. Assert the medium degradation explicitly.
    await page.addInitScript(() => {
      Reflect.deleteProperty(window, "getWindowSegments");
    });
    await page.setViewportSize({ width: 900, height: 800 });
    await page.reload();
    await expect(page.getByTestId("open-agent-slideover")).toBeVisible({
      timeout: libraryTimeout,
    });
    await expect(page.getByTestId("fold-pane-main")).toHaveCount(0);
  });
});

test.describe("adaptive expanded regression (1440x900)", () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test("keeps the free-window desktop with mission control and dock", async ({ page }) => {
    test.setTimeout(120_000);
    const stamp = String(Date.now());
    await page.goto("/");
    await expect(page.locator(".mission-control")).toBeVisible({ timeout: libraryTimeout });
    await expect(page.getByTestId("adaptive-bottom-nav")).toHaveCount(0);
    await expect(page.getByTestId("fold-pane-main")).toHaveCount(0);

    await page.getByLabel("Project name").fill(`Adaptive Expanded ${stamp}`);
    await page.getByRole("button", { name: "Create space" }).click();
    await expect(page.locator(".project-card.active")).toContainText(`Adaptive Expanded ${stamp}`);

    await page.getByTestId("open-system-monitor").click();
    await expect(
      page.locator(".workos-window", { hasText: "System Monitor" }).first(),
    ).toBeVisible();
    await expect(page.getByText(/No incidents recorded for this project\./)).toBeVisible({
      timeout: libraryTimeout,
    });
  });
});
