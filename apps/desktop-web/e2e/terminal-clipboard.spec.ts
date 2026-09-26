import { expect, test, type Page } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";

const projectId = "01999999-9999-7999-8999-000000000001";
const terminalId = "01999999-9999-7999-8999-000000000044";
const attachmentId = "01999999-9999-7999-8999-000000000045";
const terminalText = "fixture@workos:$ printf '中文🙂\\tline 2\\n'\n中文🙂\tline 2\n$ ";
const captureDir = process.env.WORKOS_TERMINAL_CAPTURE_DIR;

async function openTerminal(page: Page, controls = true) {
  const fixture = await sharedDesktopFixture({
    kind: "terminal",
    projectId,
    workloadId: terminalId,
  });
  const writes: Uint8Array[] = [];
  try {
    await fixture.install(page);
    await page.route("**/AttachSurface", async (route) => {
      await route.fulfill({
        json: {
          session: { id: terminalId, workloadId: terminalId, workloadGeneration: "3" },
          attachment: {
            id: attachmentId,
            workloadId: terminalId,
            surfaceSessionId: terminalId,
            controls,
            controlGeneration: "2",
            controlExpiresAt: "2026-09-06T09:05:00Z",
          },
        },
      });
    });
    await page.route("**/ReadPtySession", async (route) => {
      const request = route.request().postDataJSON() as { after?: string };
      const first = !request.after || request.after === "0";
      await route.fulfill({
        json: {
          output: first ? Buffer.from(terminalText).toString("base64") : "",
          cursor: String(Buffer.byteLength(terminalText)),
          closed: false,
        },
      });
    });
    await page.route("**/WritePtySession", async (route) => {
      const request = route.request().postDataJSON() as {
        sessionId: string;
        input: string;
        controlGeneration: string;
      };
      expect(request.sessionId).toBe(terminalId);
      expect(request.controlGeneration).toBe("2");
      const bytes = Buffer.from(request.input, "base64");
      expect(bytes.length).toBeLessThanOrEqual(16 * 1024);
      writes.push(bytes);
      await route.fulfill({ json: { session: { id: terminalId, state: "running" } } });
    });
    await page.goto("/");
    const output = page.getByTestId("terminal-output");
    await expect(output).toContainText("中文🙂\tline 2");
    await expect(page.getByTestId("terminal-controls-state")).toHaveText(
      controls ? "controlling" : "observer",
    );
    return { fixture, writes, output };
  } catch (error) {
    await fixture.close();
    throw error;
  }
}

test("captures the fixed Terminal clipboard controls", async ({ page }) => {
  test.skip(!captureDir, "explicit deterministic visual capture");
  await page.setViewportSize({ width: 1440, height: 900 });
  const view = await openTerminal(page);
  try {
    await page.screenshot({
      path: `${captureDir ?? "/captures"}/terminal-window--clipboard-controls--1440x900.png`,
      animations: "disabled",
    });
  } finally {
    await page.goto("about:blank");
    await view.fixture.close();
  }
});

test("Chromium copies terminal selection, interrupts without selection, and pastes ordered UTF-8", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(test.info().project.use.baseURL ?? "http://127.0.0.1:5177").origin,
  });
  const view = await openTerminal(page);
  try {
    await view.output.evaluate((element) => {
      element.focus();
      const range = document.createRange();
      range.selectNodeContents(element);
      const selection = window.getSelection();
      selection?.removeAllRanges();
      selection?.addRange(range);
    });
    await page.keyboard.press("Control+c");
    await expect(page.getByTestId("terminal-clipboard-result")).toContainText("Copied");
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(terminalText);
    expect(view.writes).toHaveLength(0);

    await view.output.evaluate((element) => {
      window.getSelection()?.removeAllRanges();
      element.focus();
    });
    await page.keyboard.press("Control+c");
    await expect.poll(() => view.writes.length).toBe(1);
    expect(Buffer.concat(view.writes).toString("hex")).toBe("03");

    const pasted = "中文🙂\tline one\nline two\n".repeat(1500);
    expect(Buffer.byteLength(pasted)).toBeGreaterThan(16 * 1024);
    await page.evaluate(async (text) => navigator.clipboard.writeText(text), pasted);
    await page.keyboard.press("Control+v");
    await expect(page.getByTestId("terminal-clipboard-result")).toContainText("Pasted");
    expect(Buffer.concat(view.writes.slice(1)).toString("utf8")).toBe(pasted);
  } finally {
    await page.goto("about:blank");
    await view.fixture.close();
  }
});

test("observer clipboard paste cannot write to the PTY", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(test.info().project.use.baseURL ?? "http://127.0.0.1:5177").origin,
  });
  const view = await openTerminal(page, false);
  try {
    await page.evaluate(() => navigator.clipboard.writeText("observer input"));
    await view.output.focus();
    await page.keyboard.press("Control+v");
    await expect(page.getByTestId("terminal-clipboard-result")).toContainText("control");
    expect(view.writes).toHaveLength(0);
  } finally {
    await page.goto("about:blank");
    await view.fixture.close();
  }
});
