import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";

const capture = process.env.WORKOS_CANVAS_CAPTURE_DIR;
test.use({ viewport: { width: 1440, height: 900 } });

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`empty desktop creation and global tools ${String(width)}x${String(height)}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    await page.route("**/ListProjects", (route) => route.fulfill({ json: { projects: [] } }));
    await page.route("**/CreateProject", (route) =>
      route.fulfill({
        json: {
          project: {
            id: "01999999-9999-7999-8999-000000000003",
            name: "First project",
            revision: "1",
          },
        },
      }),
    );
    await page.goto("/");
    if (width === 820) await page.getByTestId("open-agent-slideover").click();
    else if (width < 1024)
      await page.getByRole("button", { name: "Agent Center", exact: true }).first().click();
    if (process.env.WORKOS_CANVAS_BASELINE && capture) {
      await expect(page.getByLabel("Agent goal")).toBeDisabled();
      await page.screenshot({
        path: `${capture}/desktop--empty--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
      return;
    }
    await expect(page.getByRole("heading", { name: "A space for your next idea" })).toBeVisible();
    if (capture)
      await page.screenshot({
        path: `${capture}/desktop--empty--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    await openDesktopApp(page, "system-monitor");
    await expect(page.getByRole("button", { name: "Close System Monitor" })).toBeVisible();
    await page.getByRole("button", { name: "Close System Monitor" }).click();
    await page.getByRole("button", { name: "Create a project", exact: true }).click();
    await page.getByLabel("New project name").fill("First project");
    await page.getByRole("button", { name: "Create project", exact: true }).click();
    await expect(page.getByRole("button", { name: "Switch project" })).toContainText(
      "First project",
    );
    await page.getByRole("button", { name: "First project", exact: true }).click();
    await expect(page.getByTestId("mission-control")).toHaveCount(0);
    await expect(page.getByLabel("Agent goal")).toBeEnabled();
  });
}

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`Home launches tools and returns to the workspace ${String(width)}x${String(height)}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height });
    test.skip(!!process.env.WORKOS_CANVAS_BASELINE, "new interaction assertions");
    await desktopFixture(page);
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Switch project" })).toContainText("Studio");
    await openDesktopApp(page, "home");
    const home = page.getByTestId("home-app");
    const extent = await home.evaluate((element) => ({
      height: element.clientHeight,
      content: element.scrollHeight,
    }));
    if (width === 1440) expect(extent.content).toBeLessThanOrEqual(extent.height);
    await expect(home.getByTestId("home-entry-terminal")).toBeDisabled();
    await home.getByTestId("home-entry-settings").click();
    await expect(page.getByRole("heading", { name: "Harness provider" })).toBeVisible();
    await openDesktopApp(page, "mission-control");
    await page.getByRole("button", { name: "Field notes", exact: true }).click();
    await expect(page.getByTestId("mission-control")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Switch project" })).toContainText("Field notes");
    await expect(page.locator(".project-sidebar")).toHaveCount(0);
  });
}
