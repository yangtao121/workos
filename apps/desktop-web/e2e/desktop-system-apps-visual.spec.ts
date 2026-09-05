import { expect, test } from "@playwright/test";

// Visual capture for the W6 desktop system-apps slice
// (docs/tasks/20260903-v1-remaining-capability-sweep.md). Run explicitly:
//   pnpm exec playwright test desktop-system-apps-visual.spec.ts
// with WORKOS_CAPTURE_DIR pointing at the task's docs/ui capture folder.
// Fixed deterministic states only: the palette, Mission Control, the Home
// launchpad, the sandboxed Browser, and a snapped window, at the fixed
// 1440x900 desktop viewport plus one compact 390x844 palette frame.

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "/captures";

test("captures the desktop system-apps states", async ({ page }) => {
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(180_000);

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Capture ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Capture");

  // Mission Control with real project cards.
  await page.getByTestId("open-mission-control").click();
  await expect(page.getByTestId("mission-control")).toBeVisible();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: `${captureDir}/mission-control--projects--1440x900.png`,
  });

  // Command Palette over the fixed action set.
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("command-palette")).toBeVisible();
  await page.getByLabel("Search commands").fill("open");
  await page.waitForTimeout(200);
  await page.screenshot({
    path: `${captureDir}/command-palette--open--1440x900.png`,
  });
  await page.keyboard.press("Escape");

  // Home launchpad with the honest Terminal verdict.
  await page.getByTestId("open-home").click();
  await expect(page.getByTestId("home-app")).toBeVisible();
  await page.waitForTimeout(200);
  await page.screenshot({
    path: `${captureDir}/home--launchpad--1440x900.png`,
  });

  // Sandbox boundary frame of the Browser app.
  await page.getByTestId("home-entry-browser").click();
  const browser = page.getByTestId("browser-app");
  await expect(browser).toBeVisible();
  await browser.getByLabel("Browser address").fill("https://example.invalid/");
  await browser.getByRole("button", { name: "Go" }).click();
  await expect(browser.getByTestId("browser-frame")).toBeVisible();
  await page.screenshot({
    path: `${captureDir}/browser--sandboxed--1440x900.png`,
  });

  // Snap: the Docs window snapped to the exact left half.
  await page.getByTestId("open-docs").click();
  await expect(page.getByTestId("docs-app")).toBeVisible();
  const docsId = "docs";
  await page.getByTestId(`snap-left-${docsId}`).click();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: `${captureDir}/desktop-system-apps--snap-left--1440x900.png`,
  });

  // Compact layout: the palette stays reachable at 390x844.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(400);
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByTestId("command-palette")).toBeVisible();
  await page.waitForTimeout(200);
  await page.screenshot({
    path: `${captureDir}/command-palette--open--390x844.png`,
  });

  expect(true).toBe(true);
});
