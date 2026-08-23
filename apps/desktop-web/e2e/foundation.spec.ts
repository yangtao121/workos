import { expect, test } from "@playwright/test";

test("creates a project and runs a durable fake harness task", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E");
  await page.getByLabel("Agent goal").fill("verify the WorkOS foundation");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
});
