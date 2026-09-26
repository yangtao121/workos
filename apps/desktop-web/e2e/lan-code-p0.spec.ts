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

// This gate talks only to the running HTTPS Gateway and real Code child. The
// runner installs the LAN CA in Chromium's NSS profile. No mocks, TLS bypass,
// credential-bearing screenshots, video or traces are allowed here.
const origin = process.env.WORKOS_E2E_TLS_URL ?? "";
const username = process.env.WORKOS_LAN_E2E_USERNAME ?? "";
const passwordFile = process.env.WORKOS_LAN_E2E_PASSWORD_FILE ?? "";
const projectId = process.env.WORKOS_LAN_P0_PROJECT_ID ?? "";
const stateFile = process.env.WORKOS_LAN_P0_STATE_FILE ?? "";
const savedFile = process.env.WORKOS_LAN_P0_SAVED_FILE ?? "";
const restartFile = process.env.WORKOS_LAN_P0_RESTART_FILE ?? "";
const performanceFile = process.env.WORKOS_LAN_P0_PERFORMANCE_FILE ?? "";
const originalSha256 = process.env.WORKOS_LAN_P0_ORIGINAL_SHA256 ?? "";
const phase = process.env.WORKOS_LAN_P0_PHASE ?? "";
const relativeFile = "README.md";
const nativeService = "/workos.surface.v1.NativeSessionService";
const continuityService = "/workos.surface.v1.SurfaceContinuityService";

test.skip(process.env.WORKOS_LAN_CODE_P0_E2E !== "true", "run via tools/lan/test-code-p0.sh");
test.use({ trace: "off", screenshot: "off", video: "off" });

type Workload = {
  workloadId: string;
  projectId: string;
  renderer: string;
  appInstanceId: string;
  generation: string;
  state: string;
  attachmentCount: number;
};
type Control = {
  controlGeneration: string;
  controllerAttachmentId: string;
  controllerDeviceId: string;
  workloadRunning: boolean;
};
type State = {
  projectId: string;
  workloadId: string;
  generation: string;
  windowId: string;
  unsavedMarker: string;
  originalSha256: string;
};

async function rpcResult(
  page: Page,
  path: string,
  data: object = {},
): Promise<{ status: number; body: unknown }> {
  return page.evaluate(
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
}

async function rpc<T>(page: Page, path: string, data: object = {}): Promise<T> {
  const result = await rpcResult(page, path, data);
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

async function profile(
  browser: Browser,
  deviceScaleFactor = 1,
): Promise<{ context: BrowserContext; page: Page }> {
  const context = await browser.newContext({
    baseURL: origin,
    ignoreHTTPSErrors: false,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor,
    locale: "en-US",
    timezoneId: "UTC",
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  const page = await context.newPage();
  return { context, page };
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
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    project.project.name,
  );
}

async function nativeWorkloads(page: Page): Promise<Workload[]> {
  const listed = await rpc<{ workloads: Workload[] }>(
    page,
    `${continuityService}/ListProjectSurfaces`,
    { projectId },
  );
  return listed.workloads.filter(
    (workload) => workload.renderer === "SURFACE_RENDERER_REMOTE_NATIVE" && !workload.appInstanceId,
  );
}

async function runningWorkload(page: Page): Promise<Workload> {
  const live = (await nativeWorkloads(page)).filter((workload) => workload.state === "running");
  expect(live).toHaveLength(1);
  if (!live[0]) throw new Error("isolated project has no unique running native workload");
  return live[0];
}

async function workload(page: Page, workloadId: string): Promise<Workload> {
  const response = await rpc<{ workload: Workload }>(
    page,
    `${continuityService}/GetSurfaceWorkload`,
    { workloadId },
  );
  return response.workload;
}

async function control(page: Page, workloadId: string): Promise<Control> {
  return await rpc<{
    controlGeneration: string;
    controllerAttachmentId: string;
    controllerDeviceId: string;
    workloadRunning: boolean;
  }>(page, `${continuityService}/GetSurfaceControl`, { workloadId });
}

async function openNewNative(page: Page) {
  await openDesktopApp(page, "home");
  const entry = page.getByTestId("home-entry-native");
  await expect(entry).toBeEnabled();
  await entry.click();
}

async function openRunningNative(page: Page, workloadId: string) {
  await openDesktopApp(page, "home");
  const row = page.getByTestId("running-apps").locator(`[data-workload-id="${workloadId}"]`);
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole("button", { name: "Open", exact: true }).click();
}

async function codeWindow(page: Page): Promise<Locator> {
  const window = page
    .locator(".workos-window")
    .filter({ has: page.getByTestId("greenfield-window-canvas") })
    .filter({ has: page.locator(".window-identity strong", { hasText: /Code/i }) })
    .first();
  await expect(window).toBeVisible({ timeout: 90_000 });
  await expect(window.locator('[data-frame-state="ready"]')).toBeVisible({ timeout: 30_000 });
  await expect(window.locator(".greenfield-window-app")).toHaveAttribute(
    "data-controller",
    /^(true|false)$/,
  );
  const stats = await window.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const canvas = node as HTMLCanvasElement;
    const context = canvas.getContext("2d");
    if (!context || canvas.width < 320 || canvas.height < 220) return { pixels: 0, variance: 0 };
    const data = context.getImageData(0, 0, canvas.width, canvas.height).data;
    const xStep = Math.max(1, Math.floor(canvas.width / 80));
    const yStep = Math.max(1, Math.floor(canvas.height / 60));
    let sum = 0;
    let squares = 0;
    let pixels = 0;
    for (let y = 0; y < canvas.height; y += yStep) {
      for (let x = 0; x < canvas.width; x += xStep) {
        const index = (y * canvas.width + x) * 4;
        const light = ((data[index] ?? 0) + (data[index + 1] ?? 0) + (data[index + 2] ?? 0)) / 3;
        sum += light;
        squares += light * light;
        pixels++;
      }
    }
    return { pixels, variance: squares / pixels - (sum / pixels) ** 2 };
  });
  expect(stats.pixels).toBeGreaterThan(1000);
  expect(stats.variance).toBeGreaterThan(10);
  return window;
}

async function nativeWindowId(window: Locator, sessionId: string, generation: string) {
  const identity = await window.getAttribute("data-window-id");
  const match = identity?.match(
    new RegExp(`^native-window-${sessionId}-${generation}-([0-9a-f-]{36})$`),
  );
  expect(Boolean(match)).toBe(true);
  return match?.[1] ?? "";
}

async function focusCode(window: Locator) {
  const canvas = window.getByTestId("greenfield-window-canvas");
  const bounds = await canvas.boundingBox();
  if (!bounds) throw new Error("Code editor canvas has no visible bounds");
  // The Explorer occupies the left quarter of the native Code window. Keep
  // keyboard focus in the central editor, away from the right Chat panel.
  await canvas.click({ position: { x: bounds.width * 0.55, y: bounds.height * 0.36 } });
  await expect(window.getByLabel("原生窗口输入")).toBeFocused();
}

async function clickCodeNative(window: Locator, x: number, y: number) {
  const canvas = window.getByTestId("greenfield-window-canvas");
  const bounds = await canvas.boundingBox();
  if (!bounds) throw new Error("Code canvas has no visible bounds");
  const frame = await canvas.evaluate((node) => ({
    width: (node as HTMLCanvasElement).width,
    height: (node as HTMLCanvasElement).height,
    dpr: devicePixelRatio,
  }));
  await canvas.click({
    position: {
      x: (x / (frame.width / frame.dpr)) * bounds.width,
      y: (y / (frame.height / frame.dpr)) * bounds.height,
    },
  });
}

async function dismissCodeOnboarding(page: Page, window: Locator) {
  const canvas = window.getByTestId("greenfield-window-canvas");
  const bounds = await canvas.boundingBox();
  if (!bounds) throw new Error("Code onboarding canvas has no visible bounds");
  // A new official Code profile can show two Copilot welcome pages. Their
  // lower-right buttons were measured in the pinned Code child fixture. On an
  // already configured profile these clicks land in the editor, then the
  // following Quick Open assertion still proves the intended native state.
  await canvas.click({ position: { x: bounds.width * 0.73, y: bounds.height * 0.75 } });
  await page.waitForTimeout(500);
  await canvas.click({ position: { x: bounds.width * 0.76, y: bounds.height * 0.75 } });
}

async function paste(page: Page, window: Locator, value: string) {
  await page.evaluate(async (text) => navigator.clipboard.writeText(text), value);
  await window.getByRole("button", { name: "粘贴到应用" }).click();
  await expect(window.locator(".greenfield-window-actions [role='status']")).toContainText(
    "已向原生应用发送粘贴指令",
  );
}

async function copiedText(page: Page, window: Locator): Promise<string> {
  await window.getByRole("button", { name: "复制到本机" }).click();
  await expect(window.locator(".greenfield-window-actions [role='status']")).toContainText(
    "已将当前原生剪贴板文本复制到本机",
  );
  return page.evaluate(() => navigator.clipboard.readText());
}

async function nativePixels(window: Locator): Promise<number[]> {
  // Read only the decoded real Code frame. No fixture, screenshot or RPC ack
  // can satisfy a visible popup or scroll assertion.
  return window.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const source = node as HTMLCanvasElement;
    const sample = document.createElement("canvas");
    sample.width = 160;
    sample.height = 120;
    const context = sample.getContext("2d", { willReadFrequently: true });
    if (!context || !source.width || !source.height)
      throw new Error("native Code frame unavailable");
    context.drawImage(source, 0, 0, sample.width, sample.height);
    return Array.from(context.getImageData(0, 0, sample.width, sample.height).data);
  });
}

function changedNativePixels(before: number[], after: number[]): number {
  if (before.length !== after.length) throw new Error("native Code frame sample changed size");
  let changed = 0;
  for (let index = 0; index < before.length; index += 4) {
    const difference =
      Math.abs((before[index] ?? 0) - (after[index] ?? 0)) +
      Math.abs((before[index + 1] ?? 0) - (after[index + 1] ?? 0)) +
      Math.abs((before[index + 2] ?? 0) - (after[index + 2] ?? 0));
    if (difference > 60) changed++;
  }
  return changed;
}

async function openEditorFile(page: Page, window: Locator) {
  await dismissCodeOnboarding(page, window);
  await focusCode(window);
  await page.keyboard.press("Escape");
  const before = await nativePixels(window);
  await page.keyboard.press("Control+p");
  await expect
    .poll(async () => changedNativePixels(before, await nativePixels(window)), {
      message: "A02: Code Quick Open must appear in real native pixels",
      timeout: 10_000,
    })
    .toBeGreaterThan(120);
  await paste(page, window, relativeFile);
  // The paste toolbar button owns browser focus. Restore the viewer's hidden
  // native input without clicking the canvas, which would dismiss Quick Open.
  await window.getByLabel("原生窗口输入").focus();
  await page.keyboard.press("Enter");
  await expect(window.locator(".window-identity strong")).toContainText(relativeFile, {
    timeout: 30_000,
  });
}

async function dialogClose(browser: Browser) {
  const state = await readState();
  const current = await profile(browser);
  try {
    await signIn(current.page, await secret());
    await selectProject(current.page);
    await openRunningNative(current.page, state.workloadId);
    const code = await codeWindow(current.page);
    expect(await nativeWindowId(code, state.workloadId, state.generation)).toBe(state.windowId);
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    await dismissCodeOnboarding(current.page, code);
    // The pinned Code File menu's Open File item is a native XWayland dialog.
    // The isolated child probe proved this menu path and the resulting child
    // frame even when Ctrl+O delivery varied during fresh Code startup.
    await clickCodeNative(code, 90, 50);
    await clickCodeNative(code, 150, 182);
    const dialog = current.page
      .locator(".workos-window")
      .filter({ has: current.page.locator(".window-identity strong", { hasText: /^Open File$/ }) })
      .filter({ has: current.page.getByTestId("greenfield-window-canvas") });
    await expect(dialog).toBeVisible({ timeout: 15_000 });
    await expect(dialog.locator('[data-frame-state="ready"]')).toBeVisible({ timeout: 15_000 });
    const childId = await nativeWindowId(dialog, state.workloadId, state.generation);
    expect(childId).not.toBe(state.windowId);
    await dialog.locator(".window-close").click();
    // Desktop must leave this child mounted until Runtime reports removal.
    await expect(dialog).toHaveCount(0, { timeout: 15_000 });
    expect(await nativeWindowId(code, state.workloadId, state.generation)).toBe(state.windowId);
    await expect(code).toBeVisible();
    const live = await workload(current.page, state.workloadId);
    expect(live.state).toBe("running");
    expect(live.generation).toBe(state.generation);
    await closeViewer(code);
  } finally {
    await current.context.close();
  }
}

async function closeViewer(window: Locator) {
  await window.locator(".window-close").click();
  await expect(window).toHaveCount(0);
}

async function readState(): Promise<State> {
  const state = JSON.parse(await readFile(stateFile, "utf8")) as State;
  expect(state.projectId).toBe(projectId);
  expect(state.workloadId).toMatch(/^[0-9a-f-]{36}$/);
  expect(BigInt(state.generation)).toBeGreaterThan(0n);
  expect(state.unsavedMarker).toMatch(/^WORKOS_P0_UNSAVED_[0-9a-f-]{36}$/);
  return state;
}

async function nativeGeometry(window: Locator) {
  return window.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const canvas = node as HTMLCanvasElement;
    const stage = canvas.closest(".greenfield-window-input-stage");
    if (!stage) throw new Error("native Code stage unavailable");
    const bounds = stage.getBoundingClientRect();
    return {
      dpr: devicePixelRatio,
      cssWidth: bounds.width,
      cssHeight: bounds.height,
      frameWidth: canvas.width,
      frameHeight: canvas.height,
    };
  });
}

async function dprTwoAndResize(browser: Browser, state: State) {
  const current = await profile(browser, 2);
  try {
    await signIn(current.page, await secret());
    await selectProject(current.page);
    await openRunningNative(current.page, state.workloadId);
    const code = await codeWindow(current.page);
    expect(await nativeWindowId(code, state.workloadId, state.generation)).toBe(state.windowId);
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    await openEditorFile(current.page, code);
    await expect
      .poll(
        async () => {
          const size = await nativeGeometry(code);
          return (
            size.dpr === 2 &&
            size.cssWidth > 320 &&
            size.cssHeight > 220 &&
            Math.abs(size.frameWidth / size.cssWidth - 2) < 0.2 &&
            Math.abs(size.frameHeight / size.cssHeight - 2) < 0.2
          );
        },
        { message: "A03: DPR 2 must produce a native-resolution Code frame", timeout: 15_000 },
      )
      .toBe(true);
    const before = await nativeGeometry(code);
    await code.locator(".window-resize").press("ArrowLeft");
    await expect
      .poll(
        async () => {
          const size = await nativeGeometry(code);
          return (
            size.cssWidth < before.cssWidth - 5 &&
            size.frameWidth < before.frameWidth - 10 &&
            Math.abs(size.frameWidth / size.cssWidth - 2) < 0.2 &&
            Math.abs(size.frameHeight / size.cssHeight - 2) < 0.2
          );
        },
        { message: "A03: resizing must deliver a fresh DPR 2 native Code frame", timeout: 15_000 },
      )
      .toBe(true);
    const after = await nativeGeometry(code);
    await codeWindow(current.page);
    await writeFile(
      stateFile.replace(/state\.json$/, "dpr2.json"),
      `${JSON.stringify({ viewport: { width: 1440, height: 900 }, before, after })}\n`,
      { flag: "wx", mode: 0o600 },
    );
    await closeViewer(code);
  } finally {
    await current.context.close();
  }
}

async function unsaved(browser: Browser) {
  expect(originalSha256).toMatch(/^[0-9a-f]{64}$/);
  const marker = `WORKOS_P0_UNSAVED_${randomUUID()}`;
  const current = await profile(browser);
  let state: State | undefined;
  try {
    await signIn(current.page, await secret());
    await selectProject(current.page);
    expect(
      (await nativeWorkloads(current.page)).filter((item) => item.state === "running"),
    ).toHaveLength(0);
    await openNewNative(current.page);
    const code = await codeWindow(current.page);
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    const live = await runningWorkload(current.page);
    const session = await rpc<{ session: { engine: string; id: string } }>(
      current.page,
      `${nativeService}/GetNativeSession`,
      { sessionId: live.workloadId },
    );
    expect(session.session.engine).toBe("greenfield");
    expect(session.session.id).toBe(live.workloadId);
    await openEditorFile(current.page, code);
    await focusCode(code);
    await current.page.keyboard.press("Control+End");
    await paste(current.page, code, `\n${marker}\n`);
    await focusCode(code);
    await current.page.keyboard.press("Control+a");
    expect((await copiedText(current.page, code)).includes(marker)).toBe(true);
    state = {
      projectId,
      workloadId: live.workloadId,
      generation: live.generation,
      windowId: await nativeWindowId(code, live.workloadId, live.generation),
      unsavedMarker: marker,
      originalSha256,
    };
    await closeViewer(code);
    expect((await workload(current.page, live.workloadId)).state).toBe("running");
  } finally {
    await current.context.close();
  }
  await writeFile(stateFile, `${JSON.stringify(state)}\n`, { flag: "wx", mode: 0o600 });
}

async function continuity(browser: Browser) {
  const state = await readState();
  const secretValue = await secret();
  const first = await profile(browser);
  const second = await profile(browser);
  let savedEvidence: { payload: string; deniedMarker: string } | undefined;
  try {
    await signIn(first.page, secretValue);
    await selectProject(first.page);
    await openRunningNative(first.page, state.workloadId);
    const firstCode = await codeWindow(first.page);
    expect(await nativeWindowId(firstCode, state.workloadId, state.generation)).toBe(
      state.windowId,
    );
    await expect(firstCode.locator(".greenfield-window-app")).toHaveAttribute(
      "data-controller",
      "true",
    );
    await focusCode(firstCode);
    await first.page.keyboard.press("Control+a");
    expect((await copiedText(first.page, firstCode)).includes(state.unsavedMarker)).toBe(true);

    await signIn(second.page, secretValue);
    await selectProject(second.page);
    // Shared Desktop normally projects the Core native anchor immediately.
    // If it has not arrived yet, open the exact running workload from Home.
    if ((await second.page.getByTestId("greenfield-window-canvas").count()) === 0)
      await openRunningNative(second.page, state.workloadId);
    let secondCode = await codeWindow(second.page);
    expect(await nativeWindowId(secondCode, state.workloadId, state.generation)).toBe(
      state.windowId,
    );
    await expect(secondCode.locator(".greenfield-window-app")).toHaveAttribute(
      "data-controller",
      "false",
    );
    await expect(secondCode.getByRole("button", { name: "粘贴到应用" })).toBeDisabled();
    const beforeControl = await control(first.page, state.workloadId);
    expect(beforeControl.controllerAttachmentId).not.toBe("");
    await secondCode.getByTestId("greenfield-take-control").click();
    await expect(secondCode.locator(".greenfield-window-app")).toHaveAttribute(
      "data-controller",
      "true",
    );
    const afterControl = await control(second.page, state.workloadId);
    expect(BigInt(afterControl.controlGeneration)).toBeGreaterThan(
      BigInt(beforeControl.controlGeneration),
    );
    expect(afterControl.controllerAttachmentId).not.toBe(beforeControl.controllerAttachmentId);
    // A stale controller must fail before an arbitrary old input sequence is
    // applied. The rejected marker must never enter the native buffer.
    const denied = `WORKOS_P0_DENIED_${randomUUID()}`;
    const stale = await rpcResult(
      first.page,
      "/workos.surface.v1.GreenfieldWindowService/SendGreenfieldWindowInput",
      {
        sessionId: state.workloadId,
        attachmentId: beforeControl.controllerAttachmentId,
        expectedWorkloadGeneration: state.generation,
        controlGeneration: beforeControl.controlGeneration,
        events: [{ sequence: "1", windowId: state.windowId, text: { text: denied } }],
      },
    );
    expect(
      stale.status === 403 ||
        (stale.status === 200 &&
          (stale.body as { verdict?: string } | undefined)?.verdict ===
            "GREENFIELD_INPUT_VERDICT_STALE_CONTROL"),
    ).toBe(true);
    await second.context.setOffline(true);
    await expect(secondCode.locator("[data-frame-state]")).not.toHaveAttribute(
      "data-frame-state",
      "ready",
      { timeout: 30_000 },
    );
    await second.context.setOffline(false);
    secondCode = await codeWindow(second.page);
    expect(await nativeWindowId(secondCode, state.workloadId, state.generation)).toBe(
      state.windowId,
    );
    // A disconnected viewer may return after its renewable control lease has
    // expired. Explicitly acquire a fresh generation before sending input.
    if (
      (await secondCode.locator(".greenfield-window-app").getAttribute("data-controller")) !==
      "true"
    ) {
      await secondCode.getByTestId("greenfield-take-control").click();
    }
    await expect(secondCode.locator(".greenfield-window-app")).toHaveAttribute(
      "data-controller",
      "true",
    );
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+End");
    const composed = `WORKOS_P0_IME_${randomUUID()}_中文🙂`;
    await secondCode.getByLabel("原生窗口输入").evaluate((node, text) => {
      node.dispatchEvent(new CompositionEvent("compositionstart", { data: "", bubbles: true }));
      node.dispatchEvent(new CompositionEvent("compositionend", { data: text, bubbles: true }));
    }, composed);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+a");
    expect((await copiedText(second.page, secondCode)).split(composed)).toHaveLength(2);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+End");
    // The keyup lands outside the native input stage. The viewer must release
    // its held modifier on blur so plain Code typing still reaches the file.
    await second.page.keyboard.down("Control");
    await secondCode.locator(".window-identity strong").click();
    await second.page.keyboard.up("Control");
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+End");
    const released = `workos_p0_release_${randomUUID().replaceAll("-", "")}`;
    await second.page.keyboard.type(released);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+a");
    expect((await copiedText(second.page, secondCode)).includes(released)).toBe(true);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+End");
    const saved = `WORKOS_P0_SAVED_${randomUUID()}`;
    const mixed = "中文\t🙂 abcdefghijklmnop\n".repeat(256);
    const payload = `\n${saved}\n${mixed}`;
    expect(Array.from(payload).length).toBeGreaterThanOrEqual(4096);
    await paste(second.page, secondCode, payload);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+s");
    const beforeScroll = await nativePixels(secondCode);
    await secondCode.getByTestId("greenfield-window-canvas").hover();
    await second.page.mouse.wheel(0, -12_000);
    await expect
      .poll(async () => changedNativePixels(beforeScroll, await nativePixels(secondCode)), {
        message: "A04: wheel input must visibly scroll the real Code editor",
        timeout: 10_000,
      })
      .toBeGreaterThan(120);
    await focusCode(secondCode);
    await second.page.keyboard.press("Control+a");
    expect((await copiedText(second.page, secondCode)).includes(payload)).toBe(true);
    savedEvidence = { payload, deniedMarker: denied };
    const after = await workload(second.page, state.workloadId);
    expect(after.state).toBe("running");
    expect(after.generation).toBe(state.generation);
    await closeViewer(secondCode);
  } finally {
    await Promise.all([first.context.close(), second.context.close()]);
  }
  await writeFile(savedFile, `${JSON.stringify(savedEvidence)}\n`, { flag: "wx", mode: 0o600 });
  await dprTwoAndResize(browser, state);
}

async function restart(browser: Browser) {
  const state = await readState();
  const current = await profile(browser);
  try {
    await signIn(current.page, await secret());
    await selectProject(current.page);
    await openDesktopApp(current.page, "home");
    const row = current.page
      .getByTestId("running-apps")
      .locator(`[data-workload-id="${state.workloadId}"]`);
    await expect(row).toBeVisible({ timeout: 30_000 });
    await row.getByRole("button", { name: "Stop", exact: true }).click();
    await expect
      .poll(async () => (await workload(current.page, state.workloadId)).state)
      .toBe("stopped");
    await row.getByRole("button", { name: "Restart", exact: true }).click();
    await expect
      .poll(
        async () => {
          const found = await workload(current.page, state.workloadId);
          return found.state === "running" && BigInt(found.generation) > BigInt(state.generation);
        },
        { timeout: 60_000 },
      )
      .toBe(true);
    const newWorkload = await workload(current.page, state.workloadId);
    // RunningSurfaces.Restart opens the returned native generation itself.
    // Its new Code window can cover the Home row before another click.
    const code = await codeWindow(current.page);
    const newWindowId = await nativeWindowId(code, state.workloadId, newWorkload.generation);
    expect(newWindowId).not.toBe(state.windowId);
    await writeFile(
      restartFile,
      `${JSON.stringify({ workloadId: state.workloadId, generation: newWorkload.generation, windowId: newWindowId })}\n`,
      { flag: "wx", mode: 0o600 },
    );
    await closeViewer(code);
  } finally {
    await current.context.close();
  }
}

async function measureOne(page: Page, code: Locator): Promise<number | null> {
  const canvas = code.getByTestId("greenfield-window-canvas");
  const armed = await canvas.evaluate((node) => {
    const source = node as HTMLCanvasElement;
    const editor = source.closest(".greenfield-window-input-stage")?.querySelector("textarea");
    if (!editor || document.activeElement !== editor) return false;
    const probe = document.createElement("canvas");
    probe.width = 256;
    probe.height = 192;
    const context = probe.getContext("2d", { willReadFrequently: true });
    if (!context) return false;
    const pixels = () => {
      context.drawImage(source, 0, 0, probe.width, probe.height);
      return context.getImageData(0, 0, probe.width, probe.height).data;
    };
    const baseline = pixels();
    const windowWithMeasure = window as typeof window & {
      __workosP0PixelMeasure?: Promise<number | null>;
    };
    windowWithMeasure.__workosP0PixelMeasure = new Promise<number | null>((resolve) => {
      editor.addEventListener(
        "keydown",
        () => {
          const started = performance.now();
          const check = () => {
            const image = pixels();
            let changed = 0;
            for (let offset = 0; offset < image.length; offset += 4) {
              const difference =
                Math.abs((image[offset] ?? 0) - (baseline[offset] ?? 0)) +
                Math.abs((image[offset + 1] ?? 0) - (baseline[offset + 1] ?? 0)) +
                Math.abs((image[offset + 2] ?? 0) - (baseline[offset + 2] ?? 0));
              if (difference > 60) changed++;
            }
            if (changed >= 25) resolve(performance.now() - started);
            else if (performance.now() - started >= 5000) resolve(null);
            else requestAnimationFrame(check);
          };
          requestAnimationFrame(check);
        },
        { capture: true, once: true },
      );
    });
    return true;
  });
  if (!armed) return null;
  await page.keyboard.type("MMMM", { delay: 1 });
  return page.evaluate(async () => {
    const promise = (window as typeof window & { __workosP0PixelMeasure?: Promise<number | null> })
      .__workosP0PixelMeasure;
    return promise ? await promise : null;
  });
}

async function performancePhase(browser: Browser) {
  const state = await readState();
  const restarted = JSON.parse(await readFile(restartFile, "utf8")) as {
    workloadId: string;
    generation: string;
  };
  expect(restarted.workloadId).toBe(state.workloadId);
  const current = await profile(browser);
  const samples: number[] = [];
  let verdict = "NOT_RUN";
  let reason = "Code pixel signal unavailable";
  try {
    await signIn(current.page, await secret());
    await selectProject(current.page);
    await openRunningNative(current.page, state.workloadId);
    const code = await codeWindow(current.page);
    await expect(code.locator(".greenfield-window-app")).toHaveAttribute("data-controller", "true");
    await openEditorFile(current.page, code);
    await focusCode(code);
    await current.page.keyboard.press("Control+End");
    await current.page.keyboard.press("Enter");
    const rtt: number[] = [];
    for (let index = 0; index < 5; index++) {
      rtt.push(
        await current.page.evaluate(async () => {
          const start = performance.now();
          const response = await fetch("/workos.auth.v1.DeviceService/GetCurrentDevice", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: "{}",
            cache: "no-store",
          });
          if (!response.ok) throw new Error("RTT probe failed");
          return performance.now() - start;
        }),
      );
    }
    for (let index = 0; index < 30; index++) {
      const sample = await measureOne(current.page, code);
      if (sample === null) throw new Error("A10_NOT_RUN: no stable native Code pixel change");
      samples.push(sample);
      await current.page.keyboard.press("Enter");
    }
    await focusCode(code);
    await current.page.keyboard.press("Control+a");
    const typed = (await copiedText(current.page, code)).replace(/\r\n/g, "\n");
    if (!typed.includes("MMMM\n".repeat(30))) {
      reason = "Code buffer did not contain the timed keyboard input";
      throw new Error("A10_NOT_RUN: timed keyboard input absent from real Code buffer");
    }
    const ordered = [...samples].sort((left, right) => left - right);
    const p95 = ordered[Math.ceil(0.95 * ordered.length) - 1];
    verdict = p95 !== undefined && p95 <= 100 ? "PASS" : "OVER_TARGET";
    reason = "";
    const metadata = await current.page.evaluate(() => ({
      userAgent: navigator.userAgent,
      viewport: { width: innerWidth, height: innerHeight },
      dpr: devicePixelRatio,
    }));
    await writeFile(
      performanceFile,
      `${JSON.stringify({
        verdict,
        samplesMs: samples,
        p95Ms: p95,
        rttMs: rtt,
        browser: browser.version(),
        ...metadata,
        framePath: "real resident PNG tiles to Chromium Canvas2D",
        targetMs: 100,
        method: "DOM keydown to >=25 changed 256x192 Code canvas pixels; 30 four-key samples",
      })}\n`,
      { flag: "wx", mode: 0o600 },
    );
    expect(verdict, "A10 real Code input-to-visible-pixel p95 exceeds 100 ms").toBe("PASS");
  } catch (error) {
    if (verdict === "NOT_RUN") {
      await writeFile(
        performanceFile,
        `${JSON.stringify({ verdict, reason, samplesMs: samples, targetMs: 100 })}\n`,
        { flag: "wx", mode: 0o600 },
      );
    }
    throw error;
  } finally {
    await current.context.close();
  }
}

test("real LAN Code P0 resident phase", async ({ browser }) => {
  test.setTimeout(phase === "latency" ? 6 * 60_000 : 3 * 60_000);
  expect(origin).toMatch(/^https:\/\/[0-9.]+:8443$/);
  expect(username.length).toBeGreaterThan(0);
  expect(projectId).toMatch(/^[0-9a-f-]{36}$/);
  expect(stateFile.startsWith("/run/workos/p0-results/")).toBe(true);
  if (phase === "unsaved") await unsaved(browser);
  else if (phase === "dialog-close") await dialogClose(browser);
  else if (phase === "continuity") await continuity(browser);
  else if (phase === "restart") await restart(browser);
  else if (phase === "latency") await performancePhase(browser);
  else throw new Error("unknown LAN Code P0 phase");
});
