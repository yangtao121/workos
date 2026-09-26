import { expect, test } from "@playwright/test";

const appURL = process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:5175";

test("Chromium pastes user clipboard text into a Wayland offer and commits composition once", async ({
  browser,
}) => {
  const context = await browser.newContext();
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: appURL,
  });
  const page = await context.newPage();
  await page.goto(`${appURL}/e2e/fixtures/greenfield-input.html`);
  await page.evaluate(async () => {
    await navigator.clipboard.writeText("中文\t🙂\nline 2");
  });
  await page.locator("#fixture-canvas").click({ position: { x: 200, y: 200 } });
  await page.keyboard.press("Meta+V");
  await expect(page.locator("#fixture-selection")).toHaveText("中文\t🙂\nline 2");
  await expect(page.locator("#fixture-keys")).toContainText("29:down 47:down 47:up 29:up");

  await page.locator("#fixture-input").evaluate((input) => {
    input.dispatchEvent(new CompositionEvent("compositionstart", { data: "" }));
    input.dispatchEvent(new CompositionEvent("compositionend", { data: "输入法🙂" }));
  });
  await expect(page.locator("#fixture-selection")).toHaveText("输入法🙂");
  await expect(page.locator("#fixture-keys")).toContainText(
    "29:down 47:down 47:up 29:up 29:down 47:down 47:up 29:up",
  );
  await context.close();
});
