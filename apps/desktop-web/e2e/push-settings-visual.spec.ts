import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
const directory = process.env.WORKOS_PUSH_CAPTURE_DIR;
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`push settings visual ${String(width)}x${String(height)}`, async ({ page }) => {
    if (!directory) {
      test.skip(true, "explicit push settings capture");
      return;
    }
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    await page.goto("/");
    await page.getByRole("button", { name: "Notifications", exact: true }).first().click();
    if (!(process.env.WORKOS_VISUAL_BASELINE && width < 1024))
      await expect(page.getByTestId("notification-center")).toBeVisible();
    if (!process.env.WORKOS_VISUAL_BASELINE) {
      await page.getByRole("button", { name: "Notification settings", exact: true }).click();
      await expect(page.getByText("Quiet hours", { exact: true })).toBeVisible();
    }
    await page.screenshot({
      path: `${directory}/notifications--preferences--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
  });
}
