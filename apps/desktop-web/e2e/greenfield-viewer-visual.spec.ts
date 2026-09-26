import { expect, test } from "@playwright/test";

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const appURL = process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:5175";
test.skip(!captureDir, "visual capture runs explicitly");

test("captures two complete synthetic resident native windows", async ({ browser }) => {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "zh-CN",
    timezoneId: "UTC",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await page.goto(`${appURL}/e2e/fixtures/greenfield-viewer-ready.html`);
  await expect(page.locator('[data-frame-state="ready"]')).toHaveCount(2);
  await expect(page.getByText("只读观察；当前设备没有输入控制权。")).toHaveCount(2);
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({
    path: `${captureDir}/greenfield-resident-windows--synthetic-ready--1440x900.png`,
    animations: "disabled",
  });
  await context.close();
});
