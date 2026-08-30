import { chromium, expect, test } from "@playwright/test";

// Visual capture for the LAN device pairing slice
// (docs/tasks/20260830-lan-device-pairing.md). Runs explicitly:
//   WORKOS_CAPTURE_DIR=... WORKOS_E2E_TLS_URL=... \
//     pnpm exec playwright test lan-pairing-visual.spec.ts
//
// The pairing screenshot encodes an INVALID deterministic fixture ticket
// (all-'A' secret, all-'0' fingerprint): it can never authorize anything.
// Device names are fixture values; no cookie, key, or ticket material is
// visible in any capture.

const tlsURL = process.env.WORKOS_E2E_TLS_URL ?? "https://localhost:8443";
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
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

test("captures the paired device center from deterministic Connect fixtures", async ({
  browser,
}) => {
  test.setTimeout(90_000);
  const context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport,
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "UTC",
  });
  const page = await context.newPage();

  // Device auth is intercepted before navigation. No cookie, profile key,
  // live pairing ticket, persistent database row, or wall-clock timestamp
  // participates in this visual state.
  await page.route("**/workos.auth.v1.DeviceService/*", async (route) => {
    const rpc = new URL(route.request().url()).pathname.split("/").at(-1);
    const currentDevice = {
      deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc301",
      name: "Fixture Desktop",
      deviceClass: "DEVICE_CLASS_DESKTOP",
      revision: "3",
      createdAt: "2026-08-01T10:00:00Z",
      lastAuthenticatedAt: "2026-08-30T12:00:00Z",
      isCurrent: true,
    };
    const otherDevice = {
      deviceId: "0198d7ea-2110-7c42-b659-c5e4d73bc302",
      name: "Fixture Phone",
      deviceClass: "DEVICE_CLASS_PHONE",
      revision: "2",
      createdAt: "2026-08-02T10:00:00Z",
      lastAuthenticatedAt: "2026-08-29T12:00:00Z",
      isCurrent: false,
    };
    const body =
      rpc === "ListDevices"
        ? { devices: [currentDevice, otherDevice], nextPageToken: "" }
        : { device: currentDevice, sessionExpiresAt: "2026-08-31T12:00:00Z" };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { "Connect-Protocol-Version": "1" },
      body: JSON.stringify(body),
    });
  });

  await page.goto(`${tlsURL}/`);
  await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
  await page.getByTestId("open-device-center").click();
  const center = page.getByTestId("device-center");
  await expect(center).toBeVisible();
  await expect(center).toContainText("Fixture Desktop");
  await expect(center).toContainText("Fixture Phone");
  await expect(center).toContainText("Session expires 8/31/2026, 12:00:00 PM");
  // Hide all unrelated dynamic business data while keeping the real Desktop
  // window manager, Device Center component, typography, and layout.
  await page.addStyleTag({
    content: `
      .mission-control, .dock, .error-toast,
      .system-bar .project-switcher, .system-bar .agent-status { visibility: hidden !important; }
      .workos-window:not(:has(.device-center)) { display: none !important; }
      .workos-window:has(.device-center) > header > span { visibility: hidden !important; }
    `,
  });
  await page.evaluate(async () => document.fonts.ready);
  await page.screenshot({ path: `${captureDir}/device-center--paired-devices--1440x900.png` });
  await context.close();
});
