import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";

test("embedded same-origin pages cannot read desktop storage", async ({ page }) => {
  await desktopFixture(page);
  await page.route("**/browser-isolation-fixture", async (route) => {
    await route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><html><body>
      <button id="check">Check isolation</button><output id="result">Ready</output>
      <script>
        document.querySelector('#check').onclick = () => {
          try { document.querySelector('#result').textContent = parent.sessionStorage.getItem('workos.fixture-boundary'); }
          catch { document.querySelector('#result').textContent = 'Isolated'; }
        };
      </script></body></html>`,
    });
  });
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "Notifications", exact: true }).first(),
  ).toBeVisible();
  await page.evaluate(() => {
    sessionStorage.setItem("workos.fixture-boundary", "Desktop fixture data");
  });
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByLabel("Search commands").fill("Open Browser");
  await page.keyboard.press("Enter");
  const browser = page.getByTestId("browser-app");
  await browser
    .getByLabel("Browser address")
    .fill(new URL("/browser-isolation-fixture", page.url()).href);
  await browser.getByRole("button", { name: "Go", exact: true }).click();
  const frame = page.frameLocator('[data-testid="browser-frame"]');
  await frame.getByRole("button", { name: "Check isolation" }).click();
  await expect(frame.locator("output")).toHaveText("Isolated");
});
