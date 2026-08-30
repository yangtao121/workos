import { chromium, expect, test } from "@playwright/test";

// Visual capture for the LAN device pairing slice
// (docs/tasks/20260830-lan-device-pairing.md). Runs explicitly:
//   WORKOS_CAPTURE_DIR=... WORKOS_E2E_TLS_URL=... WORKOS_E2E_PAIRING_URL=... \
//     pnpm exec playwright test lan-pairing-visual.spec.ts
//
// The pairing screenshot encodes an INVALID deterministic fixture ticket
// (all-'A' secret, all-'0' fingerprint): it can never authorize anything.
// Device names are fixture values; no cookie, key, or ticket material is
// visible in any capture.

const tlsURL = process.env.WORKOS_E2E_TLS_URL ?? "https://localhost:8443";
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const pairURL = process.env.WORKOS_E2E_PAIRING_URL ?? "";
// Grammar-valid but useless fixture ticket material.
const fixtureFragment = `#v=1&t=${"A".repeat(43)}&fp=sha256:${"0".repeat(64)}`;

test.skip(!captureDir, "visual capture runs explicitly");

const viewport = { width: 1440, height: 900 };

test("captures unpaired and pairing states", async () => {
  test.setTimeout(90_000);
  const browser = await chromium.launch();
  const context = await browser.newContext({
    ignoreHTTPSErrors: true, // test-only fixture CA; not trust-store evidence
    viewport,
  });
  const page = await context.newPage();

  await page.goto(`${tlsURL}/`);
  const gate = page.getByTestId("auth-gate");
  await expect(gate).toHaveAttribute("data-state", "unpaired", { timeout: 20_000 });
  await page.screenshot({ path: `${captureDir}/auth-gate--unpaired--1440x900.png` });

  await page.goto(`${tlsURL}/pair${fixtureFragment}`);
  const panel = page.getByTestId("pairing-panel");
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("sha256:0000");
  // Sync assertion on the page URL: the fragment must already be scrubbed.
  expect(page.url()).not.toContain("#v=1");
  await page.screenshot({ path: `${captureDir}/auth-gate--pairing--1440x900.png` });

  await context.close();
  await browser.close();
});

test("captures the paired device center", async ({ browser }) => {
  test.setTimeout(120_000);
  test.skip(!pairURL, "needs a live operator ticket");
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport });
  const page = await context.newPage();

  // Reuse a live session if this profile already paired; otherwise pair via
  // the operator ticket. (A profile key can only ever pair once — the
  // gateway rejects duplicate credentials for the same key.)
  await page.goto(`${tlsURL}/`);
  await page.waitForTimeout(3_000);
  if (!(await page.locator(".desktop-shell").count())) {
    await page.goto(pairURL);
    await page.getByLabel("Device name").fill("E2E LAN Device");
    await page.getByTestId("pairing-panel").getByRole("button", { name: "Pair device" }).click();
  }
  await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
  await page.getByTestId("open-device-center").click();
  const center = page.getByTestId("device-center");
  await expect(center).toBeVisible();
  await expect(center).toContainText("this device");
  await page.screenshot({ path: `${captureDir}/device-center--paired-devices--1440x900.png` });
  await context.close();
});
