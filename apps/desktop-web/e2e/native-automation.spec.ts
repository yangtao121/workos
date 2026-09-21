import { expect, test } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";
test.skip(process.env.WORKOS_NATIVE_E2E !== "true", "requires native six-process fixture");
const sessionId = process.env.WORKOS_NATIVE_SESSION_ID ?? "";
test("review isolated child results and resume the persisted native goal", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await openDesktopApp(page, "agent-sessions");
  await page.getByTestId(`agent-session-entry-${sessionId}`).click();
  await expect(page.getByTestId("session-delegations")).toContainText("Alpha isolated change");
  await expect(page.getByTestId("session-delegations")).toContainText("Beta isolated change");
  await page.getByRole("button", { name: "Review changes" }).first().click();
  await expect(page.getByTestId("session-delegations")).toContainText(
    /alpha child result|beta child result/,
  );
  await page.getByRole("button", { name: "Close review" }).click();
  await expect(page.getByTestId("session-goal")).toContainText("paused");
  await page.reload();
  await openDesktopApp(page, "agent-sessions");
  if ((await page.getByTestId("session-goal").count()) === 0) {
    await page.getByTestId(`agent-session-entry-${sessionId}`).click();
  }
  await page.getByRole("button", { name: "Resume goal" }).click();
  await expect(page.getByTestId("session-goal")).toContainText("blocked", { timeout: 90_000 });
  await expect(page.getByTestId("session-goal")).toContainText("4 / 4");
});
