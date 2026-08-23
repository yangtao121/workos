import { expect, test } from "@playwright/test";

test("selects DeepSeek in Project settings and executes through the local fixture", async ({
  page,
}) => {
  test.skip(
    process.env.WORKOS_DEEPSEEK_FIXTURE_E2E !== "true",
    "requires the local DeepSeek fixture stack",
  );

  await page.goto("/");
  await page.getByLabel("Project name").fill(`DeepSeek Browser Fixture ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("DeepSeek Browser Fixture");

  await page.getByRole("button", { name: "Project settings" }).click();
  const settings = page.locator(".harness-settings");
  const deepSeek = settings.getByRole("radio", { name: "Select DeepSeek Harness" });
  await expect(deepSeek).toBeEnabled();
  await expect(settings.getByText("deepseek · adapter")).toBeVisible();
  await expect(deepSeek.locator("..")).toHaveClass(/health-healthy/);
  await deepSeek.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();
  await expect(settings.getByText("revision 2")).toBeVisible();
  await expect(deepSeek).toBeChecked();

  await page.getByLabel("Agent goal").fill("prove the DeepSeek project binding fixture");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("deepseek");
  await expect(page.getByText("Run started · deepseek")).toBeVisible();
  await expect(page.getByText("fixture response")).toBeVisible();
  await expect(page.getByText("Usage · 9 in / 3 out")).toBeVisible();

  const fake = settings.getByRole("radio", { name: "Select Deterministic Fake Harness" });
  await fake.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("revision 3")).toBeVisible();
  await expect(page.getByText("Run started · deepseek")).toBeVisible();

  await page.getByLabel("Agent goal").fill("prove only new tasks use the rebound fake provider");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("fake");
  await expect(page.getByText("Run started · fake")).toBeVisible();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
});
