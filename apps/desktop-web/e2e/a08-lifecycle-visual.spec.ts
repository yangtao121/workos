import { expect, test } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";

const directory = process.env.WORKOS_A08_CAPTURE_DIR ?? "";
test.skip(!directory, "deterministic A08 visual capture");

test("recent terminal native sessions remain visible with Restart", async ({ page }) => {
  const fixture = await sharedDesktopFixture();
  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await fixture.install(page);
    await page.route("**/ListProjectSurfaces", (route) =>
      route.fulfill({
        json: {
          workloads: [
            {
              workloadId: "01999999-9999-7999-8999-000000000015",
              projectId: "01999999-9999-7999-8999-000000000001",
              renderer: 4,
              state: "stopped",
              generation: "2",
              attachmentCount: 0,
              displayName: "Native Code",
            },
            {
              workloadId: "01999999-9999-7999-8999-000000000016",
              projectId: "01999999-9999-7999-8999-000000000001",
              renderer: 4,
              state: "failed",
              generation: "1",
              attachmentCount: 0,
              displayName: "Native editor",
            },
          ],
        },
      }),
    );
    await page.goto("/");
    const list = page.getByTestId("running-apps");
    await expect(list.locator('li[data-state="stopped"]')).toContainText("Native Code");
    await expect(list.locator('li[data-state="failed"]')).toContainText("Native editor");
    await expect(list.getByRole("button", { name: "Open" }).first()).toBeDisabled();
    await expect(list.getByRole("button", { name: "Restart" })).toHaveCount(2);
    await page.evaluate(async () => document.fonts.ready);
    await page.screenshot({
      path: `${directory}/home--recent-native-terminals--1440x900.png`,
      animations: "disabled",
    });
  } finally {
    await fixture.close();
  }
});
