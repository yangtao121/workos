import { expect, test } from "@playwright/test";

test("selects Codex in Project settings and executes through the local app-server fixture", async ({
  page,
}) => {
  test.skip(
    process.env.WORKOS_CODEX_FIXTURE_E2E !== "true",
    "requires the local Codex app-server fixture stack",
  );

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Codex Browser Fixture ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Codex Browser Fixture");

  await page.getByRole("button", { name: "Project settings" }).click();
  const settings = page.locator(".harness-settings");
  const codex = settings.getByRole("radio", { name: "Select Codex Harness" });
  await expect(codex).toBeEnabled();
  await expect(settings.getByText("codex · adapter")).toBeVisible();
  await expect(codex.locator("..")).toHaveClass(/health-healthy/);
  await codex.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();
  await expect(settings.getByText("revision 2")).toBeVisible();
  await expect(codex).toBeChecked();

  await page.getByLabel("Agent goal").fill("prove the codex project binding fixture");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("codex");
  await expect(page.getByText("Run started · codex")).toBeVisible();
  await expect(
    page.getByText("codex fixture reviewed: prove the codex project binding fixture"),
  ).toBeVisible();

  const fake = settings.getByRole("radio", { name: "Select Deterministic Fake Harness" });
  await fake.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("revision 3")).toBeVisible();

  await page.getByLabel("Agent goal").fill("prove only new tasks use the rebound fake provider");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("fake");
  await expect(page.getByText("Run started · fake")).toBeVisible();
});
