import { expect, test } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";

const directory = process.env.WORKOS_NATIVE_NAMES_CAPTURE_DIR ?? "";
const legacy = process.env.WORKOS_NATIVE_NAMES_LEGACY === "true";
test.skip(!directory, "deterministic Native session name capture");

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`two Native sessions ${String(width)}x${String(height)}`, async ({ page }) => {
    const fixture = await sharedDesktopFixture();
    try {
      await page.setViewportSize({ width, height });
      await fixture.install(page);
      await page.route("**/ListProjectSurfaces", (route) =>
        route.fulfill({
          json: {
            workloads: [
              {
                workloadId: "01999999-9999-7999-8999-000000000015",
                projectId: "01999999-9999-7999-8999-000000000001",
                renderer: 4,
                state: "running",
                generation: "2",
                attachmentCount: 1,
                displayName: legacy ? "Native display" : "WorkOS Code",
              },
              {
                workloadId: "01999999-9999-7999-8999-000000000016",
                projectId: "01999999-9999-7999-8999-000000000001",
                renderer: 4,
                state: "running",
                generation: "1",
                attachmentCount: 0,
                displayName: legacy ? "Native display" : "Text Editor",
              },
            ],
          },
        }),
      );
      await page.goto("/");
      const list = page.getByTestId("running-apps");
      const rows = list.locator("li[data-workload-id]");
      await expect(rows).toHaveCount(2);
      await expect(rows.nth(0)).toContainText(legacy ? "Native display" : "WorkOS Code");
      await expect(rows.nth(1)).toContainText(legacy ? "Native display" : "Text Editor");
      await expect(list.getByRole("button", { name: "Stop" })).toHaveCount(2);
      await page.evaluate(async () => document.fonts.ready);
      await page.screenshot({
        path: `${directory}/home--two-native-apps--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    } finally {
      await fixture.close();
    }
  });
}
