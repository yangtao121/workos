import { expect, test, type BrowserContext, type Page } from "@playwright/test";

// The production-auth acceptance gate (make test-lan-pairing; ADR-0007).
// make orchestrates the phases against a real TLS gateway + real PostgreSQL:
//
//   pair    — consume the operator pairing URL (WebCrypto P-256 proof),
//             assert the __Host- cookie attributes, drive a real Core
//             request through the Desktop, and check the /surfaces/ gate
//   persist — after `docker compose restart workos-gateway-tls`, the same
//             browser profile still holds a valid session cookie
//   reauth  — with cookies cleared, the IndexedDB profile key proves a new
//             session (authentication only, no business replay)
//   revoke  — Device Center revokes the current device; the shell returns
//             to the unpaired screen and the next request fails closed
//
// The persistent browser profile (cookies + IndexedDB) lives in a host temp
// directory mounted at WORKOS_LAN_PROFILE.
//
// ignoreHTTPSErrors is a TEST-ONLY concession to the throwaway self-signed
// fixture CA. It proves nothing about the browser trust store or native
// certificate pinning; production requires a trusted certificate.

const tlsURL = process.env.WORKOS_E2E_TLS_URL ?? "https://localhost:8443";
const phase = process.env.WORKOS_LAN_PHASE ?? "";
const pairingURL = process.env.WORKOS_E2E_PAIRING_URL ?? "";
const profileDir = process.env.WORKOS_LAN_PROFILE ?? "/lan-profile";
const deviceName = process.env.WORKOS_LAN_DEVICE_NAME ?? "E2E LAN Device";
const projectName = process.env.WORKOS_LAN_PROJECT ?? "E2E LAN Project";

test.skip(!phase, "lan-pairing phases run via make test-lan-pairing only");

async function openProfile(): Promise<{ context: BrowserContext; page: Page }> {
  const { chromium } = await import("@playwright/test");
  const context = await chromium.launchPersistentContext(profileDir, {
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
  });
  const page = context.pages()[0] ?? (await context.newPage());
  return { context, page };
}

test("lan-pairing phase runs", async () => {
  test.setTimeout(120_000);
  const { context, page } = await openProfile();
  try {
    if (phase === "pair") {
      // 1. The operator URL carries the ticket only in the fragment.
      expect(pairingURL).toContain("#v=1&t=");
      await page.goto(pairingURL);
      const panel = page.getByTestId("pairing-panel");
      await expect(panel).toBeVisible();
      await page.getByLabel("Device name").fill(deviceName);
      await panel.getByRole("button", { name: "Pair device" }).click();

      // 2. The Desktop mounts only after the pairing proof verifies.
      await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });

      // 3. The address bar no longer carries the ticket fragment.
      expect(page.url()).not.toContain("#v=1");

      // 4. The session cookie exists with the exact __Host- contract and is
      // invisible to document.cookie (HttpOnly).
      const cookies = await context.cookies(tlsURL);
      const session = cookies.find(
        (cookie: { name: string }) => cookie.name === "__Host-workos_session",
      );
      expect(session, "__Host- session cookie was set").toBeTruthy();
      expect(session?.httpOnly).toBe(true);
      expect(session?.secure).toBe(true);
      expect(session?.sameSite).toBe("Strict");
      expect(session?.path).toBe("/");
      expect(await page.evaluate(() => document.cookie)).not.toContain("workos_session");

      // 5. A real business request through the Desktop proves the Gateway
      // injected the fresh device identity into Core.
      await page.getByLabel("Project name").fill(projectName);
      await page.getByRole("button", { name: "Create space" }).click();
      await expect(page.locator(".project-card.active")).toContainText(projectName);

      // 6. The surface asset route is gated by the same session: with the
      // cookie the gate passes (runtime answers 404 for the unknown
      // session); without it the request fails closed at the gateway.
      const sessionID = "0198d7ea-2110-7c42-b659-c5e4d73bc341";
      const gated = await page.evaluate(async (path) => {
        const response = await fetch(path);
        return response.status;
      }, `/surfaces/${sessionID}/`);
      expect(gated).not.toBe(401);
      expect(gated).not.toBe(403);
      const { request } = await import("@playwright/test");
      const bare = await request.newContext({ ignoreHTTPSErrors: true });
      const anonymousStatus = (await bare.get(`${tlsURL}/surfaces/${sessionID}/`)).status();
      await bare.dispose();
      expect(anonymousStatus).toBe(401);
      return;
    }

    if (phase === "persist") {
      // After the gateway restart the profile cookie still authorizes a
      // real business write.
      await page.goto(tlsURL);
      await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
      const persistName = `${projectName}-persist`;
      await page.getByLabel("Project name").fill(persistName);
      await page.getByRole("button", { name: "Create space" }).click();
      await expect(page.locator(".project-card.active")).toContainText(persistName, {
        timeout: 15_000,
      });
      return;
    }

    if (phase === "reauth") {
      // Clear cookies: only the IndexedDB profile key can re-establish a
      // session, and the shell must come back without replaying anything.
      await context.clearCookies();
      expect(
        (await context.cookies(tlsURL)).some(
          (cookie: { name: string }) => cookie.name === "__Host-workos_session",
        ),
      ).toBe(false);
      await page.goto(tlsURL);
      // The proof can finish before Playwright observes the transient Auth
      // Gate. Cookie absence above plus the authenticated shell and real
      // write below prove the persisted IndexedDB key established a fresh
      // session without relying on timing of an intermediate render.
      await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
      // The proof re-authenticated without replaying anything; prove the
      // fresh session with a real business write.
      const reauthName = `${projectName}-reauth`;
      await page.getByLabel("Project name").fill(reauthName);
      await page.getByRole("button", { name: "Create space" }).click();
      await expect(page.locator(".project-card.active")).toContainText(reauthName, {
        timeout: 15_000,
      });
      return;
    }

    if (phase === "revoke") {
      await page.goto(tlsURL);
      await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
      await page.getByTestId("open-device-center").click();
      const center = page.getByTestId("device-center");
      await expect(center).toBeVisible();
      await expect(center).toContainText(deviceName);
      await expect(center).toContainText("this device");

      // Revoke the current device: two clicks (arm, then confirm).
      await center.getByRole("button", { name: "Remove this device" }).click();
      await center.getByRole("button", { name: "Confirm: remove this device?" }).click();

      // The shell returns to the unpaired screen; nothing of the device
      // session survives.
      await expect(page.getByTestId("auth-gate")).toHaveAttribute("data-state", "unpaired", {
        timeout: 30_000,
      });
      await expect(page.locator(".desktop-shell")).toHaveCount(0);
      return;
    }

    throw new Error(`unknown WORKOS_LAN_PHASE ${phase}`);
  } finally {
    await context.close();
  }
});
