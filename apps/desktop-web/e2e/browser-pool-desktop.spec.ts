import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The Remote Browser Pool desktop consumer (ADR-0027): the Browser system
// window drives a real pool session and paints its screencast frames on a
// canvas. Runs only on the browser-pool gate stack (real Chromium runtime).
test.setTimeout(240_000);

test.skip(process.env.WORKOS_BROWSER_POOL_E2E !== "true", "requires the browser-pool gate stack");

test("Browser window renders pool frames from a real worker", async ({ page }) => {
  await page.goto("/");
  await createDesktopProject(page, `Browser Pool Desktop ${String(Date.now())}`);

  await openDesktopApp(page, "home");
  await page.getByTestId("home-entry-browser").click();
  const browser = page.getByTestId("browser-app");
  await expect(browser).toBeVisible();

  await browser.getByLabel("Browser address").fill("http://127.0.0.1:8080/?tab=start");
  await browser.getByRole("button", { name: "Go" }).click();

  const canvas = browser.getByTestId("browser-canvas");
  await expect(canvas).toBeVisible({ timeout: 60_000 });

  // A painted frame is decodable content: the canvas carries the frame size.
  await expect
    .poll(
      async () =>
        page.evaluate(() => {
          const element = document.querySelector<HTMLCanvasElement>(
            '[data-testid="browser-canvas"]',
          );
          if (!element) return { w: 0, h: 0 };
          return { w: element.width, h: element.height };
        }),
      { timeout: 60_000 },
    )
    .toEqual({ w: 1280, h: 800 });

  // Navigation through the pool updates the same session.
  await browser.getByLabel("Browser address").fill("http://127.0.0.1:8080/?tab=review");
  await browser.getByRole("button", { name: "Go" }).click();
  await expect(canvas).toBeVisible();

  // The sandboxed iframe fallback never appears while the pool is live.
  await expect(browser.getByTestId("browser-frame")).toHaveCount(0);
  await expect(browser.getByTestId("browser-pool-notice")).toHaveCount(0);
});
