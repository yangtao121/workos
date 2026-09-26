import { randomUUID } from "node:crypto";
import { lstat, readFile, writeFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { expect, test, type Locator, type Page } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

// This owner-run gate uses the real HTTPS Gateway and resident Code child. It
// waits for Desktop's automatic renewal of Runtime's 30-minute control lease.
// Passwords, source pixels, screenshots, traces and videos never enter evidence.
const origin = process.env.WORKOS_E2E_TLS_URL ?? "";
const username = process.env.WORKOS_LAN_E2E_USERNAME ?? "";
const passwordFile = process.env.WORKOS_LAN_E2E_PASSWORD_FILE ?? "";
const projectId = process.env.WORKOS_LAN_P0_PROJECT_ID ?? "";
const resultFile = process.env.WORKOS_LAN_P0_RENEW_RESULTS_FILE ?? "";
const continuity = "/workos.surface.v1.SurfaceContinuityService";
const native = "/workos.surface.v1.NativeSessionService";
const maxRenewWaitMs = 35 * 60_000;

test.skip(
  process.env.WORKOS_LAN_CODE_RENEW_E2E !== "true",
  "run through tools/lan/test-code-control-renewal.sh",
);
test.use({ trace: "off", screenshot: "off", video: "off" });

type Workload = {
  workloadId: string;
  projectId: string;
  generation: string;
  state: string;
  renderer: string;
  appInstanceId: string;
};
type Control = {
  controlGeneration: string;
  controllerAttachmentId: string;
  controllerDeviceId: string;
  controlExpiresAt?: string;
  workloadRunning: boolean;
};

async function rpc<T>(page: Page, path: string, data: object = {}): Promise<T> {
  const result = await page.evaluate(
    async ({ path, data }) => {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
        credentials: "same-origin",
      });
      return {
        status: response.status,
        body: response.ok ? ((await response.json()) as unknown) : undefined,
      };
    },
    { path, data },
  );
  if (result.status !== 200 || !result.body)
    throw new Error(`${path.split("/").at(-1) ?? "RPC"} returned HTTP ${String(result.status)}`);
  return result.body as T;
}

async function secret(): Promise<string> {
  const facts = await lstat(passwordFile);
  if (
    !facts.isFile() ||
    facts.isSymbolicLink() ||
    facts.mode & 0o077 ||
    facts.uid !== (typeof process.getuid === "function" ? process.getuid() : -1)
  )
    throw new Error("owner-only password fixture is required");
  const value = (await readFile(passwordFile, "utf8")).replace(/\r?\n$/, "");
  if (!value) throw new Error("password fixture is empty");
  return value;
}

async function signIn(page: Page, password: string) {
  await page.goto(origin);
  const form = page.getByTestId("password-login");
  await expect(form).toBeVisible();
  await form.getByLabel("Username").fill(username);
  await form.getByLabel("Password").fill(password);
  await form.getByRole("button", { name: "Sign in" }).click();
  await expect(page.locator(".desktop-shell")).toBeVisible({ timeout: 30_000 });
  expect(await page.evaluate(() => window.isSecureContext)).toBe(true);
}

async function selectProject(page: Page) {
  const project = await rpc<{ project: { id: string; name: string } }>(
    page,
    "/workos.project.v1.ProjectService/GetProject",
    { projectId },
  );
  expect(project.project.id).toBe(projectId);
  await page.getByRole("button", { name: "Switch project", exact: true }).click();
  await page.getByTestId(`mission-card-${projectId}`).click();
  await expect(page.getByTestId("mission-control")).toHaveCount(0);
}

async function runningCode(page: Page): Promise<Workload> {
  const listed = await rpc<{ workloads: Workload[] }>(page, `${continuity}/ListProjectSurfaces`, {
    projectId,
  });
  const nativeWorkloads = listed.workloads.filter(
    (item) =>
      item.state === "running" &&
      item.renderer === "SURFACE_RENDERER_REMOTE_NATIVE" &&
      !item.appInstanceId,
  );
  const applications = await Promise.all(
    nativeWorkloads.map(async (item) => ({
      workload: item,
      session: await rpc<{ session: { id: string; application: string } }>(
        page,
        `${native}/GetNativeSession`,
        { sessionId: item.workloadId },
      ),
    })),
  );
  const code = applications.filter(
    ({ workload, session }) =>
      session.session.id === workload.workloadId &&
      session.session.application === "NATIVE_APPLICATION_CODE",
  );
  expect(code, "the prepared project must have exactly one running Code workload").toHaveLength(1);
  if (!code[0]) throw new Error("running Code workload unavailable");
  return code[0].workload;
}

async function openCode(page: Page, workload: Workload): Promise<Locator> {
  await openDesktopApp(page, "home");
  const row = page
    .getByTestId("running-apps")
    .locator(`[data-workload-id="${workload.workloadId}"]`);
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const code = page
    .locator(".workos-window")
    .filter({ has: page.getByTestId("greenfield-window-canvas") })
    .filter({ has: page.locator(".window-identity strong", { hasText: /Code/i }) })
    .first();
  await expect(code).toBeVisible({ timeout: 90_000 });
  await expect(code.locator('[data-frame-state="ready"]')).toBeVisible({ timeout: 30_000 });
  const id = await code.getAttribute("data-window-id");
  expect(id).toMatch(
    new RegExp(`^native-window-${workload.workloadId}-${workload.generation}-[0-9a-f-]{36}$`),
  );
  return code;
}

async function focusCode(code: Locator) {
  const canvas = code.getByTestId("greenfield-window-canvas");
  const bounds = await canvas.boundingBox();
  if (!bounds) throw new Error("Code editor canvas has no visible bounds");
  await canvas.click({ position: { x: bounds.width * 0.55, y: bounds.height * 0.36 } });
  await expect(code.getByLabel("原生窗口输入")).toBeFocused();
}

async function openReadme(page: Page, code: Locator) {
  const canvas = code.getByTestId("greenfield-window-canvas");
  const bounds = await canvas.boundingBox();
  if (!bounds) throw new Error("Code onboarding canvas has no visible bounds");
  await canvas.click({ position: { x: bounds.width * 0.73, y: bounds.height * 0.75 } });
  await page.waitForTimeout(500);
  await canvas.click({ position: { x: bounds.width * 0.76, y: bounds.height * 0.75 } });
  await focusCode(code);
  await page.keyboard.press("Escape");
  await page.keyboard.press("Control+p");
  await page.keyboard.type("README.md");
  await page.keyboard.press("Enter");
  await expect(code.locator(".window-identity strong")).toContainText("README.md", {
    timeout: 30_000,
  });
}

async function control(page: Page, workloadId: string): Promise<Control> {
  return rpc<Control>(page, `${continuity}/GetSurfaceControl`, { workloadId });
}

function expiresAt(controlFact: Control): number {
  const expiry = Date.parse(controlFact.controlExpiresAt ?? "");
  if (!Number.isFinite(expiry)) throw new Error("server control expiry is unavailable");
  return expiry;
}

function assertSameController(current: Control, initial: Control, deviceId: string) {
  expect(current.workloadRunning).toBe(true);
  expect(current.controllerDeviceId).toBe(deviceId);
  expect(current.controllerAttachmentId).toBe(initial.controllerAttachmentId);
  expect(current.controlGeneration).toBe(initial.controlGeneration);
}

test("real Code remains writable after automatic control lease renewal", async ({ browser }) => {
  test.setTimeout(38 * 60_000);
  expect(origin).toMatch(/^https:\/\/[0-9.]+:8443$/);
  expect(username.length).toBeGreaterThan(0);
  expect(projectId).toMatch(/^[0-9a-f-]{36}$/);
  expect(resultFile.startsWith("/run/workos/p0-renew-results/")).toBe(true);
  const context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: false,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "UTC",
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const page = await context.newPage();
  try {
    await signIn(page, await secret());
    await selectProject(page);
    const device = await rpc<{ device: { deviceId: string } }>(
      page,
      "/workos.auth.v1.DeviceService/GetCurrentDevice",
    );
    expect(device.device.deviceId).toMatch(/^[0-9a-f-]{36}$/);
    const workload = await runningCode(page);
    const code = await openCode(page, workload);
    const windowId = await code.getAttribute("data-window-id");
    if ((await code.locator(".greenfield-window-app").getAttribute("data-controller")) !== "true")
      await code.getByTestId("greenfield-take-control").click();
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    await openReadme(page, code);
    const initial = await control(page, workload.workloadId);
    assertSameController(initial, initial, device.device.deviceId);
    expect(initial.controllerAttachmentId).toMatch(/^[0-9a-f-]{36}$/);
    expect(BigInt(initial.controlGeneration)).toBeGreaterThan(0n);
    const originalExpiry = expiresAt(initial);
    const waitStartedAt = Date.now();
    process.stdout.write(
      `A07 renewal: waiting up to 35 minutes; server lease expires ${new Date(originalExpiry).toISOString()}\n`,
    );
    let nextProgressAt = waitStartedAt + 5 * 60_000;
    let renewed: Control | undefined;
    while (Date.now() - waitStartedAt < maxRenewWaitMs) {
      const current = await control(page, workload.workloadId);
      assertSameController(current, initial, device.device.deviceId);
      if (Date.now() >= nextProgressAt) {
        process.stdout.write(
          `A07 renewal: ${String(Math.floor((Date.now() - waitStartedAt) / 60_000))} minutes elapsed; server lease expires ${new Date(expiresAt(current)).toISOString()}\n`,
        );
        nextProgressAt = Date.now() + 5 * 60_000;
      }
      if (expiresAt(current) > originalExpiry) {
        renewed = current;
        break;
      }
      await delay(15_000);
    }
    if (!renewed)
      throw new Error("A07: server control expiry did not advance within the renewal gate");
    process.stdout.write(
      `A07 renewal: server lease advanced after ${String(Math.floor((Date.now() - waitStartedAt) / 60_000))} minutes; new expiry ${new Date(expiresAt(renewed)).toISOString()}\n`,
    );
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    await expect(code.locator('[data-frame-state="ready"]')).toBeVisible();
    expect(await code.getAttribute("data-window-id")).toBe(windowId);
    await focusCode(code);
    await page.keyboard.press("Control+End");
    await page.keyboard.press("Enter");
    const marker = `WORKOS_P0_RENEWED_${randomUUID()}`;
    await page.keyboard.type(marker);
    await focusCode(code);
    await page.keyboard.press("Control+a");
    await code.getByRole("button", { name: "复制到本机" }).click();
    await expect(code.locator(".greenfield-window-actions [role='status']")).toContainText(
      "已将当前原生剪贴板文本复制到本机",
    );
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(copied.split(marker)).toHaveLength(2);
    const afterInput = await control(page, workload.workloadId);
    assertSameController(afterInput, initial, device.device.deviceId);
    expect(expiresAt(afterInput)).toBeGreaterThan(originalExpiry);
    expect(await code.getAttribute("data-window-id")).toBe(windowId);
    await code.locator(".window-close").click();
    await expect(code).toHaveCount(0);
    const afterClose = await rpc<{ workload: Workload }>(page, `${continuity}/GetSurfaceWorkload`, {
      workloadId: workload.workloadId,
    });
    expect(afterClose.workload.state).toBe("running");
    expect(afterClose.workload.generation).toBe(workload.generation);
    await writeFile(
      resultFile,
      `${JSON.stringify({
        verdict: "PASS",
        projectId,
        workloadId: workload.workloadId,
        workloadGeneration: workload.generation,
        windowId,
        deviceId: device.device.deviceId,
        attachmentId: initial.controllerAttachmentId,
        controlGeneration: initial.controlGeneration,
        originalExpiry: initial.controlExpiresAt,
        renewedExpiry: renewed.controlExpiresAt,
        waitedMs: Date.now() - waitStartedAt,
        browser: browser.version(),
        viewport: { width: 1440, height: 900, dpr: 1 },
        marker: "typed and copied exactly once; bytes omitted",
      })}\n`,
      { flag: "wx", mode: 0o600 },
    );
  } finally {
    await context.close();
  }
});
