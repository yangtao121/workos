import { expect, test } from "@playwright/test";

test("creates a project and runs a durable fake harness task", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E");
  await page.getByRole("button", { name: "Project settings" }).click();
  const settings = page.locator(".harness-settings");
  await expect(settings.getByText("Core default · fake")).toBeVisible();
  await expect(
    settings.getByRole("radio", { name: "Select Deterministic Fake Harness" }),
  ).toBeEnabled();
  await expect(settings.getByRole("radio", { name: "Select DeepSeek Harness" })).toBeDisabled();
  await expect(settings.getByText("Provider is disabled or misconfigured")).toBeVisible();
  await expect(settings.getByRole("radio", { name: "Use Global Default" })).toBeChecked();
  await page.getByLabel("Agent goal").fill("verify the WorkOS foundation");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("fake");
  await expect(page.getByText("Run started · fake")).toBeVisible();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
});
