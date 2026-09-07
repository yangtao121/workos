import { createDesktopProject, openDesktopApp, expectProjectRevision } from "./open-app.js";
import { expect, test } from "@playwright/test";

test("selects DeepSeek in Project settings and executes through the local fixture", async ({
  page,
}) => {
  test.skip(
    process.env.WORKOS_DEEPSEEK_FIXTURE_E2E !== "true",
    "requires the local DeepSeek fixture stack",
  );

  await page.goto("/");
  const projectId = await createDesktopProject(
    page,
    `DeepSeek Browser Fixture ${String(Date.now())}`,
  );
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    "DeepSeek Browser Fixture",
  );

  await openDesktopApp(page, "settings");
  const settings = page.locator(".harness-settings");
  const deepSeek = settings.getByRole("radio", { name: "Select DeepSeek Harness" });
  await expect(deepSeek).toBeEnabled();
  await expect(settings.getByText("deepseek · adapter")).toBeVisible();
  await expect(deepSeek.locator("..")).toHaveClass(/health-healthy/);
  await deepSeek.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();
  await expectProjectRevision(page, projectId, "2");
  await expect(deepSeek).toBeChecked();

  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("prove the DeepSeek project binding fixture");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("deepseek");
  await expect(page.getByText("Run started · deepseek")).toBeVisible();
  await expect(page.getByText("fixture response")).toBeVisible();
  await expect(page.getByText("Usage · 9 in / 3 out")).toBeVisible();

  await openDesktopApp(page, "settings");
  const fake = settings.getByRole("radio", { name: "Select Deterministic Fake Harness" });
  await fake.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expectProjectRevision(page, projectId, "3");
  await expect(page.getByText("Run started · deepseek")).toBeVisible();

  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("prove only new tasks use the rebound fake provider");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("fake");
  await expect(page.getByText("Run started · fake")).toBeVisible();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
});
