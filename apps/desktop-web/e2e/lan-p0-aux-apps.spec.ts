import { createHash, randomUUID } from "node:crypto";
import { lstat, readFile, writeFile } from "node:fs/promises";
import { expect, test, type Browser, type Locator, type Page } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

// Owner-run only: the Gateway, Runtime child, PTY and Chromium clipboard are
// real. The runner trusts the local CA; this spec has no response mocks, TLS
// bypass, screenshots, videos or traces that could capture credentials.
const origin = process.env.WORKOS_E2E_TLS_URL ?? "";
const username = process.env.WORKOS_LAN_E2E_USERNAME ?? "";
const passwordFile = process.env.WORKOS_LAN_E2E_PASSWORD_FILE ?? "";
const projectId = process.env.WORKOS_LAN_P0_PROJECT_ID ?? "";
const resultFile = process.env.WORKOS_LAN_P0_AUX_RESULTS_FILE ?? "";
const continuity = "/workos.surface.v1.SurfaceContinuityService";
const nativeSessions = "/workos.surface.v1.NativeSessionService";

test.skip(
  process.env.WORKOS_LAN_P0_AUX_E2E !== "true",
  "run through tools/lan/test-p0-aux-apps.sh",
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
type NativeEntry = { workload: Workload; application: string };

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

async function signIn(page: Page) {
  await page.goto(origin);
  const form = page.getByTestId("password-login");
  await expect(form).toBeVisible();
  await form.getByLabel("Username").fill(username);
  await form.getByLabel("Password").fill(await secret());
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

async function nativeEntries(page: Page): Promise<NativeEntry[]> {
  const listed = await rpc<{ workloads: Workload[] }>(page, `${continuity}/ListProjectSurfaces`, {
    projectId,
  });
  const natives = listed.workloads.filter(
    (workload) => workload.renderer === "SURFACE_RENDERER_REMOTE_NATIVE" && !workload.appInstanceId,
  );
  return Promise.all(
    natives.map(async (workload) => {
      const response = await rpc<{ session?: { id: string; application?: string | number } }>(
        page,
        `${nativeSessions}/GetNativeSession`,
        { sessionId: workload.workloadId },
      );
      expect(response.session?.id).toBe(workload.workloadId);
      return { workload, application: String(response.session?.application ?? "") };
    }),
  );
}

function isEditor(entry: NativeEntry) {
  return entry.application.includes("TEXT_EDITOR") || entry.application === "2";
}

function isCode(entry: NativeEntry) {
  return entry.application === "NATIVE_APPLICATION_CODE" || entry.application === "1";
}

async function getWorkload(page: Page, workloadId: string): Promise<Workload> {
  const response = await rpc<{ workload: Workload }>(page, `${continuity}/GetSurfaceWorkload`, {
    workloadId,
  });
  return response.workload;
}

async function openRunning(page: Page, workloadId: string) {
  await openDesktopApp(page, "home");
  const row = page.getByTestId("running-apps").locator(`[data-workload-id="${workloadId}"]`);
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole("button", { name: "Open", exact: true }).click();
}

function nativeWindow(page: Page, workload: Workload): Locator {
  return page.locator(
    `.workos-window[data-window-id^="native-window-${workload.workloadId}-${workload.generation}-"]`,
  );
}

async function readyNativeWindow(page: Page, workload: Workload): Promise<Locator> {
  const window = nativeWindow(page, workload);
  await expect(window).toBeVisible({ timeout: 90_000 });
  await expect(window.locator('[data-frame-state="ready"]')).toBeVisible({ timeout: 30_000 });
  const viewer = window.locator(".greenfield-window-app");
  await expect(viewer).toHaveAttribute("data-controller", /^(true|false)$/);
  if ((await viewer.getAttribute("data-controller")) === "false")
    await window.getByTestId("greenfield-take-control").click();
  await expect(viewer).toHaveAttribute("data-controller", "true", { timeout: 30_000 });
  const stats = await window.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const canvas = node as HTMLCanvasElement;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context || canvas.width < 320 || canvas.height < 220) return { samples: 0, variance: 0 };
    const data = context.getImageData(0, 0, canvas.width, canvas.height).data;
    const xStep = Math.max(1, Math.floor(canvas.width / 80));
    const yStep = Math.max(1, Math.floor(canvas.height / 60));
    let samples = 0;
    let sum = 0;
    let squares = 0;
    for (let y = 0; y < canvas.height; y += yStep) {
      for (let x = 0; x < canvas.width; x += xStep) {
        const index = (y * canvas.width + x) * 4;
        const light = ((data[index] ?? 0) + (data[index + 1] ?? 0) + (data[index + 2] ?? 0)) / 3;
        samples++;
        sum += light;
        squares += light * light;
      }
    }
    return { samples, variance: squares / samples - (sum / samples) ** 2 };
  });
  expect(stats.samples).toBeGreaterThan(1000);
  expect(stats.variance).toBeGreaterThan(10);
  return window;
}

async function editorInkPixels(window: Locator): Promise<number> {
  return window.getByTestId("greenfield-window-canvas").evaluate((node) => {
    const canvas = node as HTMLCanvasElement;
    const context = canvas.getContext("2d", { willReadFrequently: true });
    if (!context) throw new Error("Mousepad canvas is unavailable");
    // The blank document starts below the native title/menu bars. This area
    // contains only a caret before paste; it gains many dark glyph pixels
    // when Mousepad actually inserts the text.
    const left = 40;
    const top = 90;
    const right = Math.min(canvas.width, 500);
    const bottom = Math.min(canvas.height, 230);
    if (right <= left || bottom <= top) throw new Error("Mousepad document area is too small");
    const data = context.getImageData(left, top, right - left, bottom - top).data;
    let ink = 0;
    for (let index = 0; index < data.length; index += 4) {
      if (
        (data[index] ?? 255) < 170 &&
        (data[index + 1] ?? 255) < 170 &&
        (data[index + 2] ?? 255) < 170
      )
        ink++;
    }
    return ink;
  });
}

async function editorClipboard(page: Page, window: Locator): Promise<number> {
  const pastedText = `WorkOS editor ${randomUUID()}\n中文\t🙂 café\nsecond line\t終わり`;
  const typedSuffix = `-typed-${randomUUID()}`;
  const expected = `${pastedText}${typedSuffix}`;
  await window.getByTestId("greenfield-window-canvas").click({ position: { x: 250, y: 180 } });
  await expect(window.getByLabel("原生窗口输入")).toBeFocused();
  const blankInk = await editorInkPixels(window);
  await page.evaluate(async (value) => navigator.clipboard.writeText(value), pastedText);
  await window.getByRole("button", { name: "粘贴到应用" }).click();
  await expect(window.locator(".greenfield-window-actions [role='status']")).toContainText(
    "已向原生应用发送粘贴指令",
  );
  await expect
    .poll(async () => (await editorInkPixels(window)) - blankInk, {
      message: "Mousepad must render inserted text in its real document pixels",
      timeout: 15_000,
    })
    .toBeGreaterThan(100);
  // Type a fresh suffix through the actual native input path. The original
  // clipboardWrite contains only pastedText, so a stale native clipboard
  // cannot satisfy the reverse-copy assertion below.
  await window.getByTestId("greenfield-window-canvas").click({ position: { x: 250, y: 180 } });
  await expect(window.getByLabel("原生窗口输入")).toBeFocused();
  await page.keyboard.press("Control+End");
  await page.keyboard.type(typedSuffix);
  await page.evaluate(
    async (value) => navigator.clipboard.writeText(value),
    `copy-sentinel-${randomUUID()}`,
  );
  await window.getByTestId("greenfield-window-canvas").click({ position: { x: 250, y: 180 } });
  await page.keyboard.press("Control+a");
  await window.getByRole("button", { name: "复制到本机" }).click();
  await expect(window.locator(".greenfield-window-actions [role='status']")).toContainText(
    "已将当前原生剪贴板文本复制到本机",
  );
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
  return new TextEncoder().encode(expected).length;
}

async function terminalClipboard(
  page: Page,
): Promise<{ workloadId: string; generation: string; bytes: number; sha256: string }> {
  await openDesktopApp(page, "home");
  await page.getByTestId("home-entry-terminal").click();
  const terminal = page.getByTestId("terminal-app").first();
  await expect(terminal).toBeVisible({ timeout: 30_000 });
  const terminalParent = page.locator(".workos-window").filter({ has: terminal });
  const parentId = await terminalParent.getAttribute("data-window-id");
  const workloadId = parentId?.match(/^terminal-([0-9a-f-]{36})$/)?.[1];
  if (!workloadId) throw new Error("Terminal window has no workload identity");
  const generation = (await getWorkload(page, workloadId)).generation;
  const output = terminal.getByTestId("terminal-output");
  await expect
    .poll(async () => (await output.textContent())?.length ?? 0, {
      message: "the real PTY shell must produce its first prompt",
      timeout: 30_000,
    })
    .toBeGreaterThan(0);
  if ((await terminal.getByTestId("terminal-controls-state").textContent()) !== "controlling")
    await terminal.getByTestId("terminal-take-control").click();
  await expect(terminal.getByTestId("terminal-controls-state")).toContainText("controlling", {
    timeout: 30_000,
  });
  const paste = async (text: string) => {
    await page.evaluate(async (value) => navigator.clipboard.writeText(value), text);
    await terminal.getByTestId("terminal-paste").click();
    await expect(terminal.getByTestId("terminal-clipboard-result")).toContainText(
      "Pasted text into the terminal.",
      { timeout: 30_000 },
    );
  };
  await paste("stty -echo\n");
  const nonce = randomUUID();
  const payload = Array.from(
    { length: 24 },
    (_, index) => `顺序 ${String(index).padStart(2, "0")}\t🙂 café\t${nonce}\t${"x".repeat(850)}`,
  ).join("\n");
  const expected = createHash("sha256").update(`${payload}\n`, "utf8").digest("hex");
  expect((await output.textContent())?.includes(expected)).toBe(false);
  const command = `cat <<'WORKOS_AUX_END' | sha256sum\n${payload}\nWORKOS_AUX_END\n`;
  const bytes = new TextEncoder().encode(command).length;
  expect(bytes).toBeGreaterThan(16 * 1024);
  await paste(command);
  await expect
    .poll(async () => (await output.textContent())?.includes(expected) ?? false, {
      message: "large ordered Terminal paste must produce the expected SHA-256",
      timeout: 60_000,
    })
    .toBe(true);
  await output.evaluate((node, digest) => {
    const textNode = node.firstChild;
    const text = textNode?.textContent ?? "";
    const start = text.lastIndexOf(digest);
    if (!textNode || start < 0) throw new Error("Terminal digest cannot be selected");
    const range = document.createRange();
    range.setStart(textNode, start);
    range.setEnd(textNode, start + digest.length);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
  }, expected);
  await terminal.getByTestId("terminal-copy").click();
  await expect(terminal.getByTestId("terminal-clipboard-result")).toContainText(
    "Copied terminal selection to the browser clipboard.",
  );
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(expected);
  return { workloadId, generation, bytes, sha256: expected };
}

test("resident Code, Mousepad and Terminal keep independent sessions and real clipboards", async ({
  browser,
}: {
  browser: Browser;
}) => {
  test.setTimeout(360_000);
  expect(origin.startsWith("https://")).toBe(true);
  expect(username).not.toBe("");
  expect(projectId).toMatch(/^[0-9a-f-]{36}$/);
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
    await signIn(page);
    await selectProject(page);
    const before = (await nativeEntries(page)).filter(
      (entry) => entry.workload.state === "running",
    );
    const code = before.filter(isCode);
    expect(code).toHaveLength(1);
    expect(before.filter(isEditor)).toHaveLength(0);
    const codeWorkload = code[0]?.workload;
    if (!codeWorkload) throw new Error("prepared project has no running Code session");
    await openRunning(page, codeWorkload.workloadId);
    await readyNativeWindow(page, codeWorkload);

    await openDesktopApp(page, "home");
    await page.getByTestId("home-entry-text-editor").click();
    await expect
      .poll(
        async () =>
          (await nativeEntries(page)).filter(
            (entry) => entry.workload.state === "running" && isEditor(entry),
          ).length,
        { timeout: 90_000 },
      )
      .toBe(1);
    const editor = (await nativeEntries(page)).find(
      (entry) => entry.workload.state === "running" && isEditor(entry),
    );
    if (!editor) throw new Error("Mousepad session was not created");
    expect(editor.workload.workloadId).not.toBe(codeWorkload.workloadId);
    const editorWorkload = editor.workload;
    const editorWindow = await readyNativeWindow(page, editorWorkload);
    await expect(editorWindow.locator(".window-identity strong")).toContainText("Mousepad");
    const editorWindowId = await editorWindow.getAttribute("data-window-id");
    const editorClipboardBytes = await editorClipboard(page, editorWindow);
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");

    const editorParent = page.locator(
      `.workos-window[data-window-id="native-${editorWorkload.workloadId}"]`,
    );
    // The resident top level is visible while its Core lifecycle anchor is
    // minimized. Its Close action detaches that anchor without stopping GTK.
    await editorWindow.locator(".window-close").click();
    await expect(editorParent).toHaveCount(0);
    await expect(editorWindow).toHaveCount(0);
    expect((await getWorkload(page, editorWorkload.workloadId)).state).toBe("running");
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");
    await expect(nativeWindow(page, codeWorkload)).toBeVisible();

    await openDesktopApp(page, "home");
    await page.getByTestId("home-entry-text-editor").click();
    const reopened = await readyNativeWindow(page, editorWorkload);
    expect(await reopened.getAttribute("data-window-id")).toBe(editorWindowId);
    const sameEditor = (await nativeEntries(page)).filter(
      (entry) => entry.workload.state === "running" && isEditor(entry),
    );
    expect(sameEditor.map((entry) => entry.workload.workloadId)).toEqual([
      editorWorkload.workloadId,
    ]);

    const terminal = await terminalClipboard(page);
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");
    expect((await getWorkload(page, editorWorkload.workloadId)).state).toBe("running");

    const terminalParent = page.locator(
      `.workos-window[data-window-id="terminal-${terminal.workloadId}"]`,
    );
    await terminalParent.locator(".window-close").click();
    await expect(terminalParent).toHaveCount(0);
    const detachedTerminal = await getWorkload(page, terminal.workloadId);
    expect(detachedTerminal.state).toBe("running");
    expect(detachedTerminal.generation).toBe(terminal.generation);
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");
    expect((await getWorkload(page, editorWorkload.workloadId)).state).toBe("running");

    await openRunning(page, terminal.workloadId);
    const reopenedTerminal = page.locator(
      `.workos-window[data-window-id="terminal-${terminal.workloadId}"]`,
    );
    await expect(reopenedTerminal.getByTestId("terminal-app")).toBeVisible();
    if (
      (await reopenedTerminal.getByTestId("terminal-controls-state").textContent()) !==
      "controlling"
    )
      await reopenedTerminal.getByTestId("terminal-take-control").click();
    await expect(reopenedTerminal.getByTestId("terminal-controls-state")).toContainText(
      "controlling",
      { timeout: 30_000 },
    );
    await expect
      .poll(
        async () =>
          (await reopenedTerminal.getByTestId("terminal-output").textContent())?.includes(
            terminal.sha256,
          ) ?? false,
      )
      .toBe(true);
    expect((await getWorkload(page, terminal.workloadId)).generation).toBe(terminal.generation);
    await reopenedTerminal.getByTestId("terminal-stop").click();
    await expect
      .poll(async () => (await getWorkload(page, terminal.workloadId)).state, { timeout: 30_000 })
      .toBe("stopped");
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");
    expect((await getWorkload(page, editorWorkload.workloadId)).state).toBe("running");

    await openDesktopApp(page, "home");
    const editorRow = page
      .getByTestId("running-apps")
      .locator(`[data-workload-id="${editorWorkload.workloadId}"]`);
    await expect(editorRow).toBeVisible();
    await editorRow.getByRole("button", { name: "Stop", exact: true }).click();
    await expect
      .poll(async () => (await getWorkload(page, editorWorkload.workloadId)).state, {
        timeout: 30_000,
      })
      .toBe("stopped");
    expect((await getWorkload(page, codeWorkload.workloadId)).state).toBe("running");

    await writeFile(
      resultFile,
      `${JSON.stringify(
        {
          codeSessionId: codeWorkload.workloadId,
          codeGeneration: codeWorkload.generation,
          editorSessionId: editorWorkload.workloadId,
          editorGeneration: editorWorkload.generation,
          editorWindowId,
          editorClipboardBytes,
          terminalPasteBytes: terminal.bytes,
          terminalPayloadSha256: terminal.sha256,
          terminalSessionId: terminal.workloadId,
          terminalGeneration: terminal.generation,
          terminalStoppedCodeAndEditorRunning: true,
          editorStoppedCodeRunning: true,
        },
        null,
        2,
      )}\n`,
      { flag: "wx", mode: 0o600 },
    );
  } finally {
    await context.close();
  }
});
