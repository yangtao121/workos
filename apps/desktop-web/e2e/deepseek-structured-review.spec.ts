import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The DeepSeek structured review chain runs against the real compose stack
// (ADR-0011): DeepSeek bound with a server-derived credential ref, the
// structured review contract inside the versioned task envelope, the strict
// JSON review output parsed in the adapter, the atomic batch materialization
// of both canonical outputs, and the inert viewers over the Core-minted
// artifacts. Requires the local DeepSeek fixture stack with the vault
// credential stored through the real admin socket.

test("runs a structured DeepSeek review and opens both inert viewers", async ({ page }) => {
  test.skip(
    process.env.WORKOS_DEEPSEEK_STRUCTURED_E2E !== "true",
    "requires the local DeepSeek fixture stack with the vault credential",
  );

  await page.goto("/");
  await createDesktopProject(page, `Structured Review ${Date.now().toString()}`);
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    "Structured Review",
  );

  // Bind DeepSeek: the vault credential was stored by the make target, so
  // the server-derived binding carries the opaque credential ref.
  await openDesktopApp(page, "settings");
  const settings = page.locator(".harness-settings");
  const deepSeek = settings.getByRole("radio", { name: "Select DeepSeek Harness" });
  await expect(deepSeek).toBeEnabled();
  await deepSeek.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();

  // Structured run: both canonical outputs requested. The raw JSON review
  // document never appears in the timeline — only the validated summary.
  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("produce structured review");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("checkbox", { name: "Unified diff" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByLabel("Task provider snapshot")).toContainText("deepseek");
  await expect(page.getByText("Run started · deepseek")).toBeVisible();
  await expect(page.getByText("structured review completed with two artifacts")).toBeVisible();
  await expect(page.getByText("Usage · 9 in / 3 out")).toBeVisible();
  await expect(page.getByText('"workos.deepseek.review-output.v1"')).toHaveCount(0);
  await expect(page.locator('li[data-event="artifactCreated"]')).toHaveCount(2);

  // The batch-produced artifacts review read-only through ArtifactService.
  await openDesktopApp(page, "artifact-center");
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);

  // The same artifacts are durable after a full page reload.
  await page.reload();
  await openDesktopApp(page, "artifact-center");
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);
});

test("malformed structured output fails closed without timeline leakage", async ({ page }) => {
  test.skip(
    process.env.WORKOS_DEEPSEEK_STRUCTURED_E2E !== "true",
    "requires the local DeepSeek fixture stack with the vault credential",
  );

  await page.goto("/");
  await createDesktopProject(page, `Structured Failure ${Date.now().toString()}`);
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    "Structured Failure",
  );

  await openDesktopApp(page, "settings");
  const settings = page.locator(".harness-settings");
  const deepSeek = settings.getByRole("radio", { name: "Select DeepSeek Harness" });
  await expect(deepSeek).toBeEnabled();
  await deepSeek.check();
  await settings.getByRole("button", { name: "Save harness setting" }).click();
  await expect(settings.getByText("Harness setting saved.")).toBeVisible();

  await openDesktopApp(page, "agent-center");
  await page.getByLabel("Agent goal").fill("fixture malformed output");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText("Run started · deepseek")).toBeVisible();
  await expect(page.getByText(/malformed review output/i)).toBeVisible();
  await expect(page.getByText('"workos.deepseek.review-output.v1"')).toHaveCount(0);
  await expect(page.locator('li[data-event="artifactCreated"]')).toHaveCount(0);
});
