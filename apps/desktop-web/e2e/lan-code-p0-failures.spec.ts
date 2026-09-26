import { randomUUID } from "node:crypto";
import { lstat, readFile, writeFile } from "node:fs/promises";
import {
  expect,
  test,
  type Browser,
  type BrowserContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

// Real Gateway, resident Code and Chromium clipboard only. The runner imports
// the LAN CA; neither this spec nor Playwright bypasses certificate checks.
const origin = process.env.WORKOS_E2E_TLS_URL ?? "";
const username = process.env.WORKOS_LAN_E2E_USERNAME ?? "";
const passwordFile = process.env.WORKOS_LAN_E2E_PASSWORD_FILE ?? "";
const projectId = process.env.WORKOS_LAN_P0_PROJECT_ID ?? "";
const resultFile = process.env.WORKOS_LAN_P0_FAILURE_RESULTS_FILE ?? "";
const continuity = "/workos.surface.v1.SurfaceContinuityService";
const windows = "/workos.surface.v1.GreenfieldWindowService";

test.skip(
  process.env.WORKOS_LAN_CODE_P0_FAILURE_E2E !== "true",
  "run through tools/lan/test-code-p0-failures.sh",
);
test.use({ trace: "off", screenshot: "off", video: "off" });

type Workload = {
  workloadId: string;
  generation: string;
  state: string;
  renderer: string;
  appInstanceId: string;
};
type Device = { deviceId: string; revision: string; isCurrent: boolean };
type Control = {
  controlGeneration: string;
  controllerAttachmentId: string;
  controllerDeviceId: string;
};
type MediaProbe = { startedAt: number; endedAt: number; firstBytes: number; end: string };
type MediaProbes = { windows: MediaProbe; frames: MediaProbe };

async function rpcStatus(page: Page, path: string, data: object = {}): Promise<number> {
  return page.evaluate(
    async ({ path, data }) => {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
        credentials: "same-origin",
      });
      return response.status;
    },
    { path, data },
  );
}

async function rpc<T>(page: Page, path: string, data: object = {}): Promise<T> {
  const result = await page.evaluate(
    async ({ path, data }) => {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
        credentials: "same-origin",
      });
      const body: unknown = response.ok ? await response.json() : undefined;
      return { status: response.status, body };
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

async function profile(browser: Browser): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: false,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "UTC",
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  return { context, page: await context.newPage() };
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
  const response = await rpc<{ project: { id: string; name: string } }>(
    page,
    "/workos.project.v1.ProjectService/GetProject",
    { projectId },
  );
  expect(response.project.id).toBe(projectId);
  await page.getByRole("button", { name: "Switch project", exact: true }).click();
  await page.getByTestId(`mission-card-${projectId}`).click();
  await expect(page.getByTestId("mission-control")).toHaveCount(0);
}

async function runningWorkload(page: Page): Promise<Workload> {
  const listed = await rpc<{ workloads: Workload[] }>(page, `${continuity}/ListProjectSurfaces`, {
    projectId,
  });
  const live = listed.workloads.filter(
    (item) =>
      item.state === "running" &&
      item.renderer === "SURFACE_RENDERER_REMOTE_NATIVE" &&
      !item.appInstanceId,
  );
  expect(live).toHaveLength(1);
  if (!live[0]) throw new Error("prepared project has no unique running Code workload");
  return live[0];
}

async function openCode(
  page: Page,
  workload: Workload,
): Promise<{ code: Locator; windowId: string }> {
  await selectProject(page);
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
  const variance = await code.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const canvas = node as HTMLCanvasElement;
    const pixels = canvas.getContext("2d")?.getImageData(0, 0, canvas.width, canvas.height).data;
    if (!pixels || canvas.width < 320 || canvas.height < 220) return 0;
    let sum = 0;
    let squares = 0;
    let count = 0;
    const stride = Math.max(1, Math.floor(pixels.length / 4000 / 4)) * 4;
    for (let index = 0; index < pixels.length; index += stride) {
      const value =
        ((pixels[index] ?? 0) + (pixels[index + 1] ?? 0) + (pixels[index + 2] ?? 0)) / 3;
      sum += value;
      squares += value * value;
      count++;
    }
    return squares / count - (sum / count) ** 2;
  });
  expect(variance).toBeGreaterThan(10);
  const identity = await code.getAttribute("data-window-id");
  const match = identity?.match(
    new RegExp(`^native-window-${workload.workloadId}-${workload.generation}-([0-9a-f-]{36})$`),
  );
  expect(Boolean(match)).toBe(true);
  return { code, windowId: match?.[1] ?? "" };
}

async function control(page: Page, code: Locator, workload: Workload): Promise<Control> {
  if ((await code.locator(".greenfield-window-app").getAttribute("data-controller")) !== "true")
    await code.getByTestId("greenfield-take-control").click();
  await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
  const result = await rpc<Control>(page, `${continuity}/GetSurfaceControl`, {
    workloadId: workload.workloadId,
  });
  expect(result.controllerAttachmentId).toMatch(/^[0-9a-f-]{36}$/);
  expect(BigInt(result.controlGeneration)).toBeGreaterThan(0n);
  return result;
}

async function focusCode(code: Locator) {
  await code.getByTestId("greenfield-window-canvas").click({ position: { x: 220, y: 160 } });
  await expect(code.getByLabel("原生窗口输入")).toBeFocused();
}

async function copyCode(page: Page, code: Locator): Promise<string> {
  await focusCode(code);
  await page.keyboard.press("Control+a");
  await code.getByRole("button", { name: "复制到本机" }).click();
  await expect(code.locator(".greenfield-window-actions [role='status']")).toContainText(
    "已将当前原生剪贴板文本复制到本机",
  );
  return page.evaluate(() => navigator.clipboard.readText());
}

async function openReadme(page: Page, code: Locator) {
  await focusCode(code);
  await page.keyboard.press("Control+p");
  await page.keyboard.type("README.md");
  await page.keyboard.press("Enter");
  await expect(code.locator(".window-identity strong")).toContainText("README.md", {
    timeout: 30_000,
  });
}

async function clipboardFailures(page: Page, context: BrowserContext, code: Locator) {
  await openReadme(page, code);
  const oversized = `WORKOS_P0_TOO_LARGE_${randomUUID()}`;
  const tooLarge = `${oversized}${"x".repeat(1024 * 1024)}`;
  const size = Buffer.byteLength(tooLarge, "utf8");
  expect(size).toBeGreaterThan(1024 * 1024);
  await page.evaluate(async (value) => navigator.clipboard.writeText(value), tooLarge);
  await code.getByRole("button", { name: "粘贴到应用" }).click();
  await expect(code.locator(".greenfield-window-actions [role='status']")).toContainText(
    "粘贴失败：文本超过 1 MiB",
  );
  expect((await copyCode(page, code)).includes(oversized)).toBe(false);

  const denied = `WORKOS_P0_CLIPBOARD_DENIED_${randomUUID()}`;
  await page.evaluate(async (value) => navigator.clipboard.writeText(value), denied);
  await context.grantPermissions([], { origin });
  const permission = await page.evaluate(
    async () =>
      (await navigator.permissions.query({ name: "clipboard-read" as PermissionName })).state,
  );
  expect(permission).toBe("denied");
  await code.getByRole("button", { name: "粘贴到应用" }).click();
  await expect(code.locator(".greenfield-window-actions [role='status']")).toContainText(
    "粘贴失败：浏览器拒绝读取剪贴板",
  );
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  expect((await copyCode(page, code)).includes(denied)).toBe(false);
  return { oversizedBytes: size, deniedPermission: permission };
}

// Connect's server-streaming request is a five-byte envelope followed by one
// JSON message. We verify the first response envelope is data (flag 0), then
// drain without retaining native pixels or window titles. An HTTP 404/401 or
// a fresh denied request cannot count as a previously active stream closing.
async function startMediaProbes(
  page: Page,
  workload: Workload,
  windowId: string,
  attachmentId: string,
) {
  const requests = [
    {
      name: "windows",
      path: `${windows}/WatchGreenfieldWindows`,
      data: {
        sessionId: workload.workloadId,
        attachmentId,
        expectedWorkloadGeneration: workload.generation,
      },
    },
    {
      name: "frames",
      path: `${windows}/WatchGreenfieldWindowFrames`,
      data: {
        sessionId: workload.workloadId,
        attachmentId,
        expectedWorkloadGeneration: workload.generation,
        windowId,
      },
    },
  ];
  const first = await page.evaluate(async (requests) => {
    const host = window as typeof window & { __workosP0Media?: Record<string, MediaProbe> };
    const probes: Record<string, MediaProbe> = {};
    host.__workosP0Media = probes;
    await Promise.all(
      requests.map(async ({ name, path, data }) => {
        const payload = new TextEncoder().encode(JSON.stringify(data));
        const envelope = new Uint8Array(5 + payload.length);
        new DataView(envelope.buffer).setUint32(1, payload.length);
        envelope.set(payload, 5);
        const response = await fetch(path, {
          method: "POST",
          headers: {
            "Content-Type": "application/connect+json",
            "Connect-Protocol-Version": "1",
          },
          credentials: "same-origin",
          body: envelope,
        });
        if (
          response.status !== 200 ||
          !response.headers.get("content-type")?.includes("application/connect+json") ||
          !response.body
        )
          throw new Error(
            `${name} media stream failed before first frame: HTTP ${String(response.status)}`,
          );
        const reader = response.body.getReader();
        let collected = new Uint8Array(0);
        let length = -1;
        while (length < 0 || collected.length < 5 + length) {
          const chunk = await reader.read();
          if (chunk.done) throw new Error(`${name} media stream ended before data`);
          const next = new Uint8Array(collected.length + chunk.value.length);
          next.set(collected);
          next.set(chunk.value, collected.length);
          collected = next;
          if (collected.length >= 5 && length < 0) {
            if (collected[0] !== 0)
              throw new Error(`${name} media stream sent an error before data`);
            length = new DataView(collected.buffer).getUint32(1);
            if (length < 1 || length > 4 * 1024 * 1024)
              throw new Error(`${name} media envelope size is invalid`);
          }
        }
        const probe: MediaProbe = {
          startedAt: Date.now(),
          endedAt: 0,
          firstBytes: length,
          end: "",
        };
        probes[name] = probe;
        void (async () => {
          try {
            while (!(await reader.read()).done) {
              // Media bytes are discarded, never copied into test evidence.
            }
            probe.end = "eof";
          } catch {
            probe.end = "transport_closed";
          } finally {
            probe.endedAt = Date.now();
          }
        })();
      }),
    );
    return Object.fromEntries(
      Object.entries(probes).map(([name, value]) => [name, value.firstBytes]),
    );
  }, requests);
  expect(first.windows).toBeGreaterThan(0);
  expect(first.frames).toBeGreaterThan(0);
  const before = await mediaProbes(page);
  expect(before.windows.endedAt).toBe(0);
  expect(before.frames.endedAt).toBe(0);
}

async function mediaProbes(page: Page): Promise<MediaProbes> {
  return page.evaluate(() => {
    const host = window as typeof window & { __workosP0Media?: Record<string, MediaProbe> };
    const state = host.__workosP0Media;
    if (!state?.windows || !state.frames) throw new Error("both media subscriptions must exist");
    return { windows: { ...state.windows }, frames: { ...state.frames } };
  });
}

async function waitForMediaClose(page: Page, cutoffAt: number, timeout: number) {
  await expect
    .poll(
      async () => {
        const state = await mediaProbes(page);
        return state.windows.endedAt > 0 && state.frames.endedAt > 0;
      },
      { timeout, message: "both already-active Code media subscriptions must end" },
    )
    .toBe(true);
  const state = await mediaProbes(page);
  for (const probe of [state.windows, state.frames]) {
    expect(probe.startedAt).toBeLessThanOrEqual(cutoffAt);
    expect(probe.endedAt).toBeGreaterThanOrEqual(cutoffAt);
    expect(probe.endedAt - cutoffAt).toBeLessThanOrEqual(timeout);
    expect(["eof", "transport_closed"]).toContain(probe.end);
  }
  return {
    windowsMs: state.windows.endedAt - cutoffAt,
    framesMs: state.frames.endedAt - cutoffAt,
  };
}

async function noInteractiveCode(page: Page, expectedMessage: "native" | "session") {
  await expect
    .poll(
      async () => {
        const viewer = page.locator(".greenfield-window-app");
        const interactive =
          (await viewer.count()) > 0 &&
          (await viewer.first().getAttribute("data-controller")) === "true";
        const nativeError = await page.getByText("原生窗口服务不可用", { exact: true }).isVisible();
        const status = page.locator(".agent-status");
        const sessionError =
          (await status.count()) > 0 &&
          ((await status.first().textContent())?.includes("unavailable") ?? false);
        const login = await page.getByTestId("password-login").isVisible();
        return (
          !interactive &&
          (expectedMessage === "native" ? nativeError : sessionError || login || nativeError)
        );
      },
      { timeout: expectedMessage === "native" ? 15_000 : 45_000 },
    )
    .toBe(true);
  await expect(page.locator('.greenfield-window-app[data-controller="true"]')).toHaveCount(0);
}

async function logoutQuietly(page: Page) {
  try {
    await rpcStatus(page, "/workos.auth.v1.DeviceService/Logout");
  } catch {
    // A failed test must still close its isolated browser context.
  }
}

test("real Code media and clipboard fail closed after authorization loss", async ({ browser }) => {
  test.setTimeout(7 * 60_000);
  expect(origin).toMatch(/^https:\/\/[0-9.]+:8443$/);
  expect(username.length).toBeGreaterThan(0);
  expect(projectId).toMatch(/^[0-9a-f-]{36}$/);
  expect(resultFile.startsWith("/run/workos/p0-failure-results/")).toBe(true);
  const password = await secret();
  const detached = await profile(browser);
  let attachmentResult: { windowsMs: number; framesMs: number } | undefined;
  let clipboardResult: { oversizedBytes: number; deniedPermission: PermissionState } | undefined;
  try {
    await signIn(detached.page, password);
    const live = await runningWorkload(detached.page);
    const { code, windowId } = await openCode(detached.page, live);
    const held = await control(detached.page, code, live);
    clipboardResult = await clipboardFailures(detached.page, detached.context, code);
    await startMediaProbes(detached.page, live, windowId, held.controllerAttachmentId);
    const detachedAt = Date.now();
    expect(
      await rpcStatus(detached.page, `${continuity}/DetachSurface`, {
        surfaceSessionId: live.workloadId,
      }),
    ).toBe(200);
    attachmentResult = await waitForMediaClose(detached.page, detachedAt, 15_000);
    expect(
      await rpcStatus(detached.page, `${windows}/ReadGreenfieldClipboard`, {
        sessionId: live.workloadId,
        attachmentId: held.controllerAttachmentId,
        expectedWorkloadGeneration: live.generation,
        controlGeneration: held.controlGeneration,
      }),
    ).toBe(403);
    expect(
      await rpcStatus(detached.page, `${windows}/SendGreenfieldWindowInput`, {
        sessionId: live.workloadId,
        attachmentId: held.controllerAttachmentId,
        expectedWorkloadGeneration: live.generation,
        controlGeneration: held.controlGeneration,
        events: [{ sequence: "1", windowId, focus: {} }],
      }),
    ).toBe(403);
    await noInteractiveCode(detached.page, "native");
    const stillRunning = await rpc<{ workload: Workload }>(
      detached.page,
      `${continuity}/GetSurfaceWorkload`,
      { workloadId: live.workloadId },
    );
    expect(stillRunning.workload.state).toBe("running");
  } finally {
    await logoutQuietly(detached.page);
    await detached.context.close();
  }

  const admin = await profile(browser);
  const victim = await profile(browser);
  let revokeResult: { windowsMs: number; framesMs: number } | undefined;
  try {
    await signIn(admin.page, password);
    await signIn(victim.page, password);
    const victimDevice = await rpc<{ device: Device }>(
      victim.page,
      "/workos.auth.v1.DeviceService/GetCurrentDevice",
    );
    expect(victimDevice.device.isCurrent).toBe(true);
    const adminDevice = await rpc<{ device: Device }>(
      admin.page,
      "/workos.auth.v1.DeviceService/GetCurrentDevice",
    );
    expect(adminDevice.device.deviceId).not.toBe(victimDevice.device.deviceId);
    const live = await runningWorkload(victim.page);
    const { code, windowId } = await openCode(victim.page, live);
    const held = await control(victim.page, code, live);
    await startMediaProbes(victim.page, live, windowId, held.controllerAttachmentId);
    const revokedAt = Date.now();
    expect(
      await rpcStatus(admin.page, "/workos.auth.v1.DeviceService/RevokeDevice", {
        deviceId: victimDevice.device.deviceId,
        expectedRevision: victimDevice.device.revision,
        idempotencyKey: randomUUID(),
      }),
    ).toBe(200);
    revokeResult = await waitForMediaClose(victim.page, revokedAt, 40_000);
    expect(await rpcStatus(victim.page, "/workos.auth.v1.DeviceService/GetCurrentDevice")).toBe(
      401,
    );
    expect(
      await rpcStatus(victim.page, `${windows}/ReadGreenfieldClipboard`, {
        sessionId: live.workloadId,
        attachmentId: held.controllerAttachmentId,
        expectedWorkloadGeneration: live.generation,
        controlGeneration: held.controlGeneration,
      }),
    ).toBe(401);
    await noInteractiveCode(victim.page, "session");
    expect(await rpcStatus(admin.page, "/workos.auth.v1.DeviceService/GetCurrentDevice")).toBe(200);
  } finally {
    await logoutQuietly(admin.page);
    await Promise.all([admin.context.close(), victim.context.close()]);
  }
  await writeFile(
    resultFile,
    `${JSON.stringify({
      verdict: "PASS",
      viewport: { width: 1440, height: 900, dpr: 1 },
      clipboard: clipboardResult,
      attachmentLoss: attachmentResult,
      deviceRevocation: revokeResult,
      streamPaths: ["WatchGreenfieldWindows", "WatchGreenfieldWindowFrames"],
      pixels: "real resident Code",
    })}\n`,
    { flag: "wx", mode: 0o600 },
  );
});
