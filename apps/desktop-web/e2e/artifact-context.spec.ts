import { expect, test } from "@playwright/test";

// The review-artifact-as-Agent-context chain runs against the real compose
// stack (ADR-0010): the desktop pins a materialized review artifact as Agent
// context, the submit request carries the exact id+digest ref, Core verifies
// it pre-enqueue, harness-host resolves it over the private authenticated
// channel under the active lease, and the Fake provider receipts the exact
// count/order/digest deterministically. No route mocks.

test("pins a review artifact as Agent context and runs a context-bound task", async ({ page }) => {
  test.setTimeout(120_000);
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Context E2E ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Context E2E");

  // Produce one markdown review artifact to pin.
  await page.getByLabel("Agent goal").fill("produce the synthetic review documents");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("checkbox", { name: "Unified diff" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();

  // Pin the artifact as Agent context from the Artifact Center.
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  const center = page.locator(".artifact-center-body");
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);
  await center.getByTestId("use-as-context").first().click();
  await expect(center.getByTestId("context-selected").first()).toBeVisible();

  // The composer shows the removable chip (title + type only). Close the
  // center window so it no longer covers the Agent Center composer.
  await page.getByRole("button", { name: "Close Artifact Center" }).click();
  const chip = page.getByTestId("context-chip");
  await expect(chip).toHaveCount(1);
  await expect(chip).toContainText("Fake Harness Review Document");
  await expect(chip).toContainText("Markdown");
  // The digest and raw id never appear in the composer UI.
  await expect(page.locator(".task-composer")).not.toContainText("sha256:");

  // Run the context-bound task: the fake provider receipts the exact
  // document through the resolved context.
  await page.getByLabel("Agent goal").fill("review the pinned context");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(
    page.getByText(/resolved context: \[document\.markdown\.v1 artifact\.review\.v1 sha256:/),
  ).toBeVisible();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
  // The chips were consumed by the successful submit.
  await expect(page.getByTestId("context-chip")).toHaveCount(0);

  // A second project starts with an empty context set: switching projects
  // can never carry stale chips across.
  await page.getByLabel("Project name").fill(`Context E2E B ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Context E2E B");
  await expect(page.getByTestId("context-chip")).toHaveCount(0);
  await expect(page.getByTestId("context-hint")).toHaveCount(0);
});

test("caps pinned context at four with a fixed hint", async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Context Cap ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Context Cap");

  // Three runs produce five review artifacts: pinning all five exercises
  // the four-chip cap and its fixed hint.
  for (let round = 0; round < 2; round += 1) {
    await page.getByLabel("Agent goal").fill(`produce documents round ${String(round)}`);
    await page.getByRole("checkbox", { name: "Markdown document" }).check();
    await page.getByRole("checkbox", { name: "Unified diff" }).check();
    await page.getByRole("button", { name: "Run task" }).click();
    await expect(page.getByText(/completed by fake harness/)).toBeVisible();
  }
  await page.getByLabel("Agent goal").fill("produce documents round final");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  await expect(page.getByTestId("artifact-row")).toHaveCount(5);

  await expect(page.getByTestId("use-as-context")).toHaveCount(5);
  // Clicking a pin replaces it with a pinned note, so the list shrinks as we
  // go: always click the first remaining pin. The fifth attempt hits the
  // four-chip cap and leaves its button in place.
  for (let index = 0; index < 4; index += 1) {
    await page.getByTestId("use-as-context").first().click();
  }
  await expect(page.getByTestId("context-chip")).toHaveCount(4);
  // The fifth attempt hits the cap and shows the fixed hint.
  await page.getByTestId("use-as-context").first().click();
  await expect(page.getByTestId("context-hint")).toContainText("At most 4");
  // Close the center before asserting on the composer behind it.
  await page.getByRole("button", { name: "Close Artifact Center" }).click();
  await expect(page.getByTestId("context-chip")).toHaveCount(4);
  await expect(page.getByTestId("context-hint")).toContainText("At most 4");
});
