import { readFile } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { expect, test, type Browser, type BrowserContext, type Page } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

// Real LAN acceptance: this file must never mock a response or suppress TLS
// errors. tools/lan/test-browser.sh imports the host's local CA into a fresh
// Chromium trust store and supplies an operator-created password fixture.
const origin = process.env.WORKOS_E2E_TLS_URL ?? "";
const username = process.env.WORKOS_LAN_E2E_USERNAME ?? "";
const passwordFile = process.env.WORKOS_LAN_E2E_PASSWORD_FILE ?? "";
const enabled = process.env.WORKOS_LAN_PASSWORD_E2E === "true";
const surfacePath = "/surfaces/01990000-0000-7000-8000-000000000001/";
const disabledRawGreenfieldPath = "/native/greenfield/01990000-0000-7000-8000-000000000001/";

test.skip(!enabled, "run with tools/lan/test-browser.sh against the integrated LAN stack");
test.use({ trace: "off", screenshot: "off", video: "off" });

interface RPCResult<T> {
  status: number;
  body: T | undefined;
}

interface Device {
  deviceId: string;
  revision: string;
  isCurrent: boolean;
}

async function rpc<T>(page: Page, path: string, data: object = {}): Promise<RPCResult<T>> {
  return page.evaluate(
    async ({ path, data }) => {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
        credentials: "same-origin",
      });
      const body = response.ok ? ((await response.json()) as T) : undefined;
      return { status: response.status, body };
    },
    { path, data },
  );
}

async function getStatus(page: Page, path: string): Promise<number> {
  return page.evaluate(
    async (route) => (await fetch(route, { credentials: "same-origin" })).status,
    path,
  );
}

async function profile(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: false,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
  });
  const page = await context.newPage();
  return { context, page };
}

async function password(): Promise<string> {
  const raw = await readFile(passwordFile, "utf8");
  const value = raw.replace(/\r?\n$/, "");
  if (!value) throw new Error("LAN browser fixture password file is empty");
  return value;
}

async function signIn(page: Page, secret: string): Promise<void> {
  const form = page.getByTestId("password-login");
  await expect(form).toBeVisible();
  await form.getByLabel("Username").fill(username);
  await form.getByLabel("Password").fill(secret);
  await form.getByRole("button", { name: "Sign in" }).click();
  await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
}

async function currentDevice(page: Page): Promise<Device> {
  const result = await rpc<{ device: Device }>(
    page,
    "/workos.auth.v1.DeviceService/GetCurrentDevice",
  );
  expect(result.status).toBe(200);
  expect(result.body?.device.deviceId).toMatch(/^[0-9a-f-]{36}$/);
  expect(result.body?.device.isCurrent).toBe(true);
  if (!result.body?.device) throw new Error("Gateway returned no current device");
  return result.body.device;
}

async function protectedRoutesWork(page: Page): Promise<void> {
  expect((await rpc(page, "/workos.project.v1.ProjectService/ListProjects")).status).toBe(200);
  const status = await getStatus(page, surfacePath);
  expect(status).not.toBe(401);
  expect(status).not.toBe(403);
  // Production never routes the direct Greenfield proxy: its raw signaling
  // key is unavailable even to an authenticated owner.
  expect(await getStatus(page, disabledRawGreenfieldPath)).toBe(404);
}

async function protectedRoutesReject(page: Page): Promise<void> {
  expect((await rpc(page, "/workos.project.v1.ProjectService/ListProjects")).status).toBe(401);
  expect(await getStatus(page, surfacePath)).toBe(401);
  expect(await getStatus(page, disabledRawGreenfieldPath)).toBe(404);
}

async function sessionCookie(context: BrowserContext) {
  const cookies = await context.cookies(origin);
  const session = cookies.find((cookie) => cookie.name === "__Host-workos_session");
  expect(Boolean(session), "Gateway must issue the __Host- session cookie").toBe(true);
  if (!session) throw new Error("session cookie missing");
  expect(session.httpOnly).toBe(true);
  expect(session.secure).toBe(true);
  expect(session.sameSite).toBe("Strict");
  expect(session.path).toBe("/");
  expect(session.domain).toBe(new URL(origin).hostname);
  return session;
}

test("trusted LAN HTTPS password login isolates two Chromium profiles and denies revoked sessions", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  expect(origin.startsWith("https://")).toBe(true);
  expect(username.length).toBeGreaterThan(0);
  const secret = await password();
  const a = await profile(browser);
  const b = await profile(browser);
  try {
    // The first navigation proves actual Chromium trust: no insecure-context
    // override or ignoreHTTPSErrors is present in this test or runner.
    await a.page.goto(origin);
    await expect(a.page.getByTestId("password-login")).toBeVisible();
    const security = await a.page.evaluate(() => ({
      secure: window.isSecureContext,
      clipboard: typeof navigator.clipboard.readText === "function",
      webCodecs: "VideoDecoder" in window,
    }));
    expect(security).toEqual({ secure: true, clipboard: true, webCodecs: true });

    const mode = await rpc<{ mode: string }>(a.page, "/workos.auth.v1.PasswordAuthService/GetMode");
    expect(mode.status).toBe(200);
    expect(mode.body?.mode).toBe("AUTH_MODE_PASSWORD");
    await protectedRoutesReject(a.page);
    expect((await rpc(a.page, "/workos.auth.v1.DeviceAuthAdminService/SetPassword")).status).toBe(
      404,
    );
    expect((await rpc(a.page, "/workos.auth.v1.DevicePairingService/BeginPairing")).status).toBe(
      404,
    );

    // One bad password must neither mount Desktop nor create a session.
    const form = a.page.getByTestId("password-login");
    await form.getByLabel("Username").fill(username);
    await form.getByLabel("Password").fill(`${secret}-incorrect`);
    await form.getByRole("button", { name: "Sign in" }).click();
    await expect(form.getByRole("alert")).toContainText("Sign in failed");
    expect(
      (await a.context.cookies(origin)).some((cookie) => cookie.name === "__Host-workos_session"),
    ).toBe(false);

    await signIn(a.page, secret);
    const first = await currentDevice(a.page);
    const oldCookie = await sessionCookie(a.context);
    expect(await a.page.evaluate(() => document.cookie.includes("workos_session"))).toBe(false);
    await protectedRoutesWork(a.page);

    await b.page.goto(origin);
    await expect(b.page.getByTestId("password-login")).toBeVisible();
    await protectedRoutesReject(b.page);
    await signIn(b.page, secret);
    const second = await currentDevice(b.page);
    expect(second.deviceId).not.toBe(first.deviceId);
    await sessionCookie(b.context);
    await protectedRoutesWork(b.page);

    const listed = await rpc<{ devices: Device[] }>(
      a.page,
      "/workos.auth.v1.DeviceService/ListDevices",
      { pageSize: 100 },
    );
    expect(listed.status).toBe(200);
    expect(listed.body?.devices.some((device) => device.deviceId === first.deviceId)).toBe(true);
    expect(listed.body?.devices.some((device) => device.deviceId === second.deviceId)).toBe(true);
    const revoked = await rpc(a.page, "/workos.auth.v1.DeviceService/RevokeDevice", {
      deviceId: second.deviceId,
      expectedRevision: second.revision,
      idempotencyKey: randomUUID(),
    });
    expect(revoked.status).toBe(200);
    await protectedRoutesReject(b.page);
    await protectedRoutesWork(a.page);
    await b.page.reload();
    await expect(b.page.getByTestId("password-login")).toBeVisible();

    // Use the real Device Center action, then replay the old cookie with a
    // future browser expiry to prove the Gateway revoked its server session.
    await openDesktopApp(a.page, "device-center");
    const center = a.page.getByTestId("device-center");
    await expect(center.getByRole("list", { name: "Signed-in devices" })).toBeVisible();
    await expect(center.getByRole("button", { name: "Pair another device" })).toHaveCount(0);
    await center.getByRole("button", { name: "Sign out" }).click();
    await expect(a.page.getByTestId("password-login")).toBeVisible({ timeout: 30_000 });
    await a.context.addCookies([{ ...oldCookie, expires: Math.floor(Date.now() / 1000) + 3600 }]);
    await protectedRoutesReject(a.page);
    await a.page.reload();
    await expect(a.page.getByTestId("password-login")).toBeVisible();
    await signIn(a.page, secret);
    await protectedRoutesWork(a.page);
    // End the replacement session, leaving no live test browser session.
    expect((await rpc(a.page, "/workos.auth.v1.DeviceService/Logout")).status).toBe(200);
  } finally {
    await Promise.all([a.context.close(), b.context.close()]);
  }
});

test("a real expired password session cannot be revived by restoring its old cookie", async ({
  browser,
}) => {
  test.skip(
    process.env.WORKOS_LAN_PASSWORD_EXPIRY_E2E !== "true",
    "run explicitly with Gateway WORKOS_AUTH_SESSION_TTL=5m",
  );
  test.setTimeout(9 * 60_000);
  const secret = await password();
  const active = await profile(browser);
  try {
    await active.page.goto(origin);
    await signIn(active.page, secret);
    const cookie = await sessionCookie(active.context);
    const current = await rpc<{ sessionExpiresAt: string }>(
      active.page,
      "/workos.auth.v1.DeviceService/GetCurrentDevice",
    );
    expect(current.status).toBe(200);
    const expiry = Date.parse(current.body?.sessionExpiresAt ?? "");
    const remaining = expiry - Date.now();
    expect(remaining).toBeGreaterThan(0);
    expect(remaining).toBeLessThanOrEqual(6 * 60_000);
    await active.page.waitForTimeout(remaining + 2_000);
    await active.context.addCookies([{ ...cookie, expires: Math.floor(Date.now() / 1000) + 3600 }]);
    await protectedRoutesReject(active.page);
    await active.page.reload();
    await expect(active.page.getByTestId("password-login")).toBeVisible();
  } finally {
    await active.context.close();
  }
});
