import { expect, test } from "@playwright/test";

// The full review-artifact chain runs against the real compose stack:
// Gateway → Core AgentTaskService → harness-host worker (Fake provider) →
// private AppendTaskArtifact → Core Artifact PostgreSQL → public
// ArtifactService reads rendered by the desktop shell. No route mocks.

test("materializes fake harness artifacts and reviews them read-only", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Artifact E2E ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Artifact E2E");

  // Request both canonical artifact outputs for the run.
  await page.getByLabel("Agent goal").fill("produce the synthetic review documents");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("checkbox", { name: "Unified diff" }).check();
  await page.getByRole("button", { name: "Run task" }).click();

  await expect(page.getByText("Run started · fake")).toBeVisible();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
  const artifactEvents = page.locator('li[data-event="artifactCreated"]');
  await expect(artifactEvents).toHaveCount(2);

  // The markdown event opens the read-only viewer with the exact stored
  // content — through ArtifactService, not the event payload.
  await artifactEvents
    .first()
    .getByRole("button", { name: /Open review/ })
    .click();
  const viewer = page.locator(".artifact-viewer-body");
  await expect(
    viewer.getByRole("heading", { level: 1, name: "Fake Harness Review Document" }),
  ).toBeVisible();
  await expect(viewer.getByText("review fixtures stay synthetic")).toBeVisible();

  // Close the markdown window and open the diff one.
  await page.getByRole("button", { name: "Close Artifact Review" }).last().click();
  await artifactEvents
    .nth(1)
    .getByRole("button", { name: /Open review/ })
    .click();
  const diffViewer = page.locator(".artifact-viewer-body");
  await expect(diffViewer.getByText("diff --git a/src/example.ts b/src/example.ts")).toBeVisible();
  await expect(diffViewer.locator(".diff-addition")).toHaveCount(2);
  await expect(diffViewer.locator(".diff-deletion")).toHaveCount(1);
  await page.getByRole("button", { name: "Close Artifact Review" }).last().click();

  // The Artifact Center lists the current project's artifacts after the run.
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  const rows = page.getByTestId("artifact-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText("Fake Harness Review Document");

  // The same artifacts are still listed after a full page reload — they are
  // durable Project facts, not window state.
  await page.reload();
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);

  // Opening from the center goes through the same authoritative viewer.
  await page.getByTestId("artifact-row").first().click();
  await expect(
    page.locator(".artifact-viewer-body").getByRole("heading", { level: 1 }),
  ).toBeVisible();
});
