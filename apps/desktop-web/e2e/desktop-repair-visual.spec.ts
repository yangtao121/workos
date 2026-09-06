import { desktopFixture } from "./desktop-fixture.js";
import { expect, test, type Page } from "@playwright/test";

const captureDir = process.env.WORKOS_CAPTURE_DIR;
async function open(page: Page, label: string) {
  await page.keyboard.press("ControlOrMeta+k");
  await page.getByLabel("Search commands").fill(label);
  await page.keyboard.press("Enter");
}

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`desktop repair visual ${String(width)}x${String(height)}`, async ({ page }) => {
    if (!captureDir) {
      test.skip(true, "explicit deterministic visual capture");
      return;
    }
    await page.setViewportSize({ width: width, height: height });
    await desktopFixture(page);
    await page.goto("/");
    if (width === 820) await page.getByTestId("open-agent-slideover").click();
    else if (width < 1024)
      await page.getByRole("button", { name: "Agent Center", exact: true }).first().click();
    await expect(page.getByRole("textbox", { name: "Agent goal" })).toBeVisible();
    await page
      .getByRole("textbox", { name: "Agent goal" })
      .fill("Review the project notes and suggest the next three steps.");
    await page.screenshot({
      path: `${captureDir}/agent-center--composer--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
    if (width === 820)
      await page
        .getByTestId("agent-slideover")
        .getByRole("button", { name: "Close", exact: true })
        .click();
    await open(page, "Open Home");
    if (!(process.env.WORKOS_VISUAL_BASELINE && width === 820))
      await expect(page.getByTestId("home-app")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/home--launchpad--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
    await open(page, "Mission Control");
    if (!(process.env.WORKOS_VISUAL_BASELINE && width === 820))
      await expect(page.getByTestId("mission-control")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/mission-control--projects--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
    await page.keyboard.press("ControlOrMeta+k");
    await page.getByLabel("Search commands").fill("open");
    await page.screenshot({
      path: `${captureDir}/command-palette--open--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
  });
}
