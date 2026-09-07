import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";

const directory = process.env.WORKOS_TOOLS_CAPTURE_DIR;
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`project tools ${String(width)}x${String(height)}`, async ({ page }) => {
    if (!directory) {
      test.skip(true, "explicit project tools capture");
      return;
    }
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    await page.route("**/ListApps", async (route) => {
      await route.fulfill({
        json: {
          page: {},
          apps: [
            {
              id: "notes",
              name: "Project Notes",
              version: "1.0.0",
              permissions: ["artifact.read", "artifact.write"],
            },
            {
              id: "reader",
              name: "Reading Room",
              version: "1.0.0",
              permissions: ["knowledge.read"],
            },
            { id: "scratchpad", name: "Scratchpad", version: "1.0.0", permissions: [] },
          ],
        },
      });
    });
    await page.route("**/ListInstalledApps", async (route) => {
      await route.fulfill({ json: { installations: [], page: {} } });
    });
    await page.goto("/");
    await expect(
      page.getByRole("button", { name: "Notifications", exact: true }).first(),
    ).toBeVisible();
    for (const [label, surface, ready] of [
      ["App Library", "app-library", "Project Notes"],
      ["Project settings", "project-settings", "Harness provider"],
    ] as const) {
      await page.keyboard.press("ControlOrMeta+k");
      await page.getByLabel("Search commands").fill(`Open ${label}`);
      await page.keyboard.press("Enter");
      await expect(page.getByText(ready, { exact: true })).toBeVisible();
      await page.screenshot({
        path: `${directory}/${surface}--project-tools--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    }
  });
}
