import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The supervised terminal gate (ADR-0028): the real desktop Terminal window
// opens a PTY session, renders live shell output, and forwards keyboard
// input to the real login shell.
test.setTimeout(240_000);

test.skip(process.env.WORKOS_TERMINAL_E2E !== "true", "requires the terminal-sessions gate stack");

test("Terminal window runs a real supervised shell", async ({ page }) => {
  await page.goto("/");
  await createDesktopProject(page, `Terminal Desktop ${String(Date.now())}`);

  await openDesktopApp(page, "home");
  const home = page.getByTestId("home-app");
  const terminalEntry = home.getByTestId("home-entry-terminal");
  await expect(terminalEntry).toBeEnabled();
  await terminalEntry.click();

  const terminal = page.getByTestId("terminal-app");
  await expect(terminal).toBeVisible();

  // The real shell greets with a prompt; sessions start within seconds.
  const output = page.getByTestId("terminal-output");
  await expect(output).toBeVisible();
  await expect.poll(async () => output.textContent(), { timeout: 60_000 }).toContain("$");

  // Typed input reaches the real shell and echoes back.
  await output.click();
  await page.keyboard.type("echo workos-terminal-proof");
  await page.keyboard.press("Enter");
  await expect
    .poll(async () => output.textContent(), { timeout: 60_000 })
    .toContain("workos-terminal-proof");
});
