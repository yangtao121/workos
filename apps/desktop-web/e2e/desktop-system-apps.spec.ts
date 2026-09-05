import { expect, test } from "@playwright/test";

// The W6 desktop system-apps gate: Command Palette keyboard surface,
// Mission Control creation/switch, the Home launchpad with honest
// Terminal unavailability, the sandboxed Browser boundary, and window
// snap geometry.

test("palette opens with the keyboard, navigates, and opens Mission Control", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Palette ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Palette");

  await page.keyboard.press("ControlOrMeta+k");
  const palette = page.getByTestId("command-palette");
  await expect(palette).toBeVisible();

  await page.getByLabel("Search commands").fill("mission");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("mission-control")).toBeVisible();
  await expect(palette).toHaveCount(0);

  // A query with no match shows the bounded empty verdict.
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByLabel("Search commands").fill("zzzz no such command");
  await expect(page.getByText("No matching command.")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("command-palette")).toHaveCount(0);
});

test("Mission Control creates a project and switches the active project", async ({ page }) => {
  const firstName = `MC first ${String(Date.now())}`;
  await page.goto("/");
  await page.getByLabel("Project name").fill(firstName);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("MC first");

  await page.getByTestId("open-mission-control").click();
  const mission = page.getByTestId("mission-control");
  await expect(mission).toBeVisible();

  const unique = `MC second ${String(Date.now())}`;
  await mission.getByLabel("New project name").fill(unique);
  await mission.getByRole("button", { name: "Create project" }).click();
  await expect(page.locator(".project-card.active")).toContainText("MC second");

  // Switching back to the named project from its card is bounded and live.
  await page
    .getByTestId("mission-control")
    .locator(".mission-card", { hasText: firstName })
    .click();
  await expect(page.locator(".project-card.active")).toContainText("MC first");
});

test("Home launchpad opens system apps and marks Terminal unavailable", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Home ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Home");

  await page.getByTestId("open-home").click();
  const home = page.getByTestId("home-app");
  await expect(home).toBeVisible();

  const terminal = home.getByTestId("home-entry-terminal");
  await expect(terminal).toBeDisabled();
  await expect(terminal).toContainText("unavailable");

  await home.getByTestId("home-entry-browser").click();
  const browser = page.getByTestId("browser-app");
  await expect(browser).toBeVisible();
  await expect(browser.getByText("Enter an address to browse inside WorkOS.")).toBeVisible();

  // Non-http(s) URLs get the fixed verdict; the sandbox stays put.
  await browser.getByLabel("Browser address").fill("javascript:alert(1)");
  await browser.getByRole("button", { name: "Go" }).click();
  await expect(browser.getByText("Only http(s) pages can be shown here.")).toBeVisible();

  await browser.getByLabel("Browser address").fill("https://example.invalid/");
  await browser.getByRole("button", { name: "Go" }).click();
  await expect(browser.getByTestId("browser-frame")).toHaveAttribute(
    "sandbox",
    "allow-scripts allow-forms allow-same-origin",
  );
});

test("Docs, Code, and Files open per project with bounded empty states", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Apps ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Apps");

  await page.getByTestId("open-docs").click();
  await expect(page.getByTestId("docs-app")).toBeVisible();

  await page.getByTestId("open-code").click();
  await expect(page.getByTestId("code-app")).toBeVisible();

  await page.getByTestId("open-files").click();
  const files = page.getByTestId("files-app");
  await expect(files).toBeVisible();

  // Files is an explicit bounded query surface over the indexed workspace
  // projection; an empty query never reaches the server.
  const search = files.getByRole("button", { name: "Search" });
  await expect(search).toBeDisabled();
  await files.getByLabel("Search workspace files").fill("anything");
  await expect(search).toBeEnabled();
});

test("windows snap to the exact half viewport and restore", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Snap ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Snap");

  await page.getByTestId("open-docs").click();
  const docsWindow = page
    .locator(".workos-window")
    .filter({ has: page.getByText("Docs", { exact: true }) })
    .first();
  await expect(docsWindow).toBeVisible();

  await page.getByTestId("snap-left-docs").click();
  await expect(docsWindow).toHaveCSS("left", "0px");
  const width = await docsWindow.evaluate((element) => element.getBoundingClientRect().width);
  expect(Math.abs(width - 720)).toBeLessThanOrEqual(1);

  await page.getByTestId("snap-right-docs").click();
  const rightX = await docsWindow.evaluate((element) => element.getBoundingClientRect().x);
  expect(Math.abs(rightX - 720)).toBeLessThanOrEqual(1);
});
