import { expect, test } from "@playwright/test";

// Deterministic visual records for the password LAN entry. All authentication
// responses are local fixtures; no live owner account or cookie is captured.
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const appURL = process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:5174";
test.skip(!captureDir, "visual capture runs explicitly");

test("captures password entry and signed-in devices", async ({ browser }) => {
  test.setTimeout(90_000);
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "UTC",
  });
  const page = await context.newPage();
  await page.route("**/workos.auth.v1.PasswordAuthService/GetMode", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ mode: "AUTH_MODE_PASSWORD" }),
    }),
  );
  await page.route("**/workos.auth.v1.DeviceService/*", (route) => {
    const method = new URL(route.request().url()).pathname.split("/").at(-1);
    if (method === "GetCurrentDevice") {
      return route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({ code: "unauthenticated", message: "session expired" }),
      });
    }
    return route.abort();
  });

  await page.goto(appURL);
  await expect(page.getByTestId("password-login")).toBeVisible();
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({ path: `${captureDir}/auth-gate--password-login--1440x900.png` });

  await page.goto(`${appURL}/e2e/fixtures/lan-password-device-center.html`);
  const center = page.getByTestId("device-center");
  await expect(center).toContainText("Fixture Desktop");
  await expect(center.getByRole("list", { name: "Signed-in devices" })).toBeVisible();
  await expect(center.getByRole("button", { name: "Pair another device" })).toHaveCount(0);
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({ path: `${captureDir}/device-center--password-devices--1440x900.png` });
  await context.close();
});
