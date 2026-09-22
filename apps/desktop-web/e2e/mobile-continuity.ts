import { readFile } from "node:fs/promises";
import { expect, type Page } from "@playwright/test";

export async function touchHome(page: Page) {
  if (await page.getByTestId("nav-home").isVisible()) {
    await page.getByTestId("nav-home").tap();
  } else {
    await page.getByTestId("toggle-dock").tap();
    await page
      .getByTestId("adaptive-dock")
      .getByRole("button", { name: "Home", exact: true })
      .tap();
  }
}

export async function mobileContinuation(phone: Page, tablet: Page) {
  for (const page of [phone, tablet]) {
    expect(await page.evaluate(() => navigator.maxTouchPoints)).toBeGreaterThan(0);
    await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
      "Development",
    );
    await touchHome(page);
    await page.getByRole("button", { name: "Agent Sessions", exact: true }).tap();
    await page
      .getByTestId("agent-sessions-app")
      .getByRole("button")
      .filter({ hasText: "V2_DEVELOP_1" })
      .tap();
    await expect(page.getByTestId("agent-session-view")).toContainText("V2_DEVELOP_1");
  }
  await tablet.getByLabel("Session message").fill("V2_DEVELOP_2 change to triple and update tests");
  await tablet.getByRole("button", { name: "Send", exact: true }).tap();
  await expect
    .poll(() => readFile("/workos-gate/project/calculate.cjs", "utf8"), { timeout: 120000 })
    .toBe("module.exports = n => n * 3;\n");
  await expect(
    tablet
      .locator(".session-input", { hasText: "V2_DEVELOP_2" })
      .getByTestId("session-input-state"),
  ).toHaveText("done", { timeout: 60000 });
  await phone.reload();
  await touchHome(phone);
  await phone.getByRole("button", { name: "Agent Sessions", exact: true }).tap();
  const view = phone.getByTestId("agent-session-view");
  if (!(await view.isVisible())) {
    await phone
      .getByTestId("agent-sessions-app")
      .getByRole("button")
      .filter({ hasText: "V2_DEVELOP_1" })
      .tap();
  }
  await expect(view).toContainText("V2_DEVELOP_2");
  await expect(
    view.locator(".session-input", { hasText: "V2_DEVELOP_2" }).getByTestId("session-input-state"),
  ).toHaveText("done");
  console.log("MOBILE_TOUCH_PROJECT_AGENT_CONTINUATION_PASS");
}
