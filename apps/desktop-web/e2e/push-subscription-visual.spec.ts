import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
const directory = process.env.WORKOS_PUSH_STATE_CAPTURE_DIR;
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`revoked browser subscription ${String(width)}x${String(height)}`, async ({ page }) => {
    if (!directory) {
      test.skip(true, "explicit subscription state capture");
      return;
    }
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    const key = `BA${"A".repeat(85)}`;
    await page.addInitScript((key) => {
      const subscription = {
        endpoint: "https://push.fixture.test/browser",
        options: {
          applicationServerKey: Uint8Array.from(atob(key), (c) => c.charCodeAt(0)).buffer,
        },
      };
      Object.defineProperty(navigator.serviceWorker, "getRegistration", {
        value: () =>
          Promise.resolve({
            pushManager: { getSubscription: () => Promise.resolve(subscription) },
          }),
      });
    }, key);
    await page.route("**/GetPushPreferences", async (route) => {
      await route.fulfill({
        json: {
          preferences: {
            revision: "1",
            quietEnabled: true,
            quietStartUtc: "22:00",
            quietEndUtc: "07:00",
          },
          webPushPublicKey: key,
          webPushSubscriptionDigest: "",
        },
      });
    });
    await page.goto("/");
    await page.getByRole("button", { name: "Notifications", exact: true }).first().click();
    await page.getByRole("button", { name: "Notification settings", exact: true }).click();
    await expect(
      page.getByRole("button", {
        name: process.env.WORKOS_VISUAL_BASELINE
          ? "Disable browser alerts"
          : "Reconnect browser alerts",
      }),
    ).toBeVisible();
    await page.screenshot({
      path: `${directory}/notifications--subscription-reconnect--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
  });
}
