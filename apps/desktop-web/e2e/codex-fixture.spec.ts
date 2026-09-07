import { createDesktopProject, openDesktopApp, expectProjectRevision } from "./open-app.js";
import { expect, test } from "@playwright/test";

test("selects Codex in Project settings and executes through the local app-server fixture", async ({
  page,
}) => {
  test.skip(
    process.env.WORKOS_CODEX_FIXTURE_E2E !== "true",
    "requires the local Codex app-server fixture stack",
  );

  await page.goto("/");
  const projectId = await createDesktopProject(page, `Codex Browser Fixture ${String(Date.now())}`);
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    "Codex Browser Fixture",
  );

  await openDesktopApp(page, "settings");
  const settings = page.locator(".harness-settings");
  const codex = settings.getByRole("radio", { name: "Select Codex Harness" });
  await expect(codex).toBeEnabled();
  await expect(settings.getByText("codex · adapter")).toBeVisible();
  await expect(codex.locator("..")).toHaveClass(/health-healthy/);
  await codex.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();
  await expectProjectRevision(page, projectId, "2");
  await expect(codex).toBeChecked();

  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("prove the codex project binding fixture");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("codex");
  await expect(page.getByText("Run started · codex")).toBeVisible();
  await expect(
    page.getByText("codex fixture reviewed: prove the codex project binding fixture"),
  ).toBeVisible();

  await openDesktopApp(page, "settings");
  const fake = settings.getByRole("radio", { name: "Select Deterministic Fake Harness" });
  await fake.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expectProjectRevision(page, projectId, "3");

  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("prove only new tasks use the rebound fake provider");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("fake");
  await expect(page.getByText("Run started · fake")).toBeVisible();
});
