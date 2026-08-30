// Deterministic visual evidence for the review-artifact-as-Agent-context
// slice (ADR-0010), captured per docs/ui/README.md: fixed Chromium
// 1440x900 @ deviceScaleFactor 1, synthetic fixtures only.
// WORKOS_CAPTURE_DIR selects the task's docs/ui capture folder.
import { expect, test } from "@playwright/test";

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "/captures";
const stamp = Date.now().toString();

test("captures the agent context surfaces", async ({ page }) => {
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(180_000);
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Context Visual ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Context Visual");

  await page.getByLabel("Agent goal").fill("produce the synthetic review documents");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("checkbox", { name: "Unified diff" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/)).toBeVisible();

  // Artifact Center with the Use-as-context actions.
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);
  await page.getByTestId("use-as-context").first().click();
  await expect(
    page.locator(".artifact-center-body").getByTestId("context-selected").first(),
  ).toBeVisible();
  await page.screenshot({ path: `${captureDir}/artifact-center--use-as-context--1440x900.png` });

  // Agent Center composer with the pinned chip.
  const chip = page.getByTestId("context-chip");
  await expect(chip).toHaveCount(1);
  await page.screenshot({ path: `${captureDir}/agent-center--context-chip--1440x900.png` });
});
