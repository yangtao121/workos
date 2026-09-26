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
  await expect(page.getByTestId("greenfield-take-control")).toHaveCount(2);
  await expect(page.getByTestId("greenfield-take-control").first()).toBeEnabled();
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({
    path: `${captureDir}/greenfield-resident-windows--synthetic-ready--1440x900.png`,
    animations: "disabled",
  });
  await context.close();
});

test("captures visible takeover after a rejected resident input", async ({ browser }) => {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "zh-CN",
    timezoneId: "UTC",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await page.goto(`${appURL}/e2e/fixtures/greenfield-viewer-ready.html?lease-loss=1`);
  await expect(page.locator('[data-frame-state="ready"]')).toHaveCount(2);
  await expect(page.getByTestId("greenfield-take-control")).toHaveCount(2);
  await expect(page.getByText("只读观察；当前设备没有输入控制权。")).toHaveCount(2);
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({
    path: `${captureDir}/greenfield-resident-windows--control-lost--1440x900.png`,
    animations: "disabled",
  });
  await context.close();
});

test("captures a native dialog while its exact close request awaits Runtime removal", async ({
  browser,
}) => {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "zh-CN",
    timezoneId: "UTC",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await page.goto(`${appURL}/e2e/fixtures/greenfield-viewer-ready.html?close-pending=1`);
  await expect(page.locator('[data-frame-state="ready"]')).toHaveCount(2);
  await expect(page.getByRole("button", { name: "Close Dialog fixture window" })).toBeDisabled();
  await expect(page.getByText("正在关闭…")).toBeVisible();
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({
    path: `${captureDir}/greenfield-resident-windows--close-pending--1440x900.png`,
    animations: "disabled",
  });
  await context.close();
});

test("captures the native child after the Code parent receives local focus", async ({
  browser,
}) => {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "zh-CN",
    timezoneId: "UTC",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await page.goto(`${appURL}/e2e/fixtures/greenfield-viewer-ready.html?parent-focused=1`);
  await expect(page.locator('[data-frame-state="ready"]')).toHaveCount(2);
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({
    path: `${captureDir}/greenfield-resident-windows--parent-focus--1440x900.png`,
    animations: "disabled",
  });
  const front = await page.evaluate(() =>
    document.elementFromPoint(800, 400)?.closest(".workos-window")?.getAttribute("data-window-id"),
  );
  expect(front).toBe("01999999-9999-7999-8999-000000000013");
  await context.close();
});
