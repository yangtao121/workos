import { expect, test } from "@playwright/test";

// This spec is run only by the owner knowledge gate while the Indexer
// container is deliberately stopped. It proves that the outage is isolated:
// Core/Gateway/Harness work remains usable and Knowledge Center reports a
// bounded unavailable state instead of inventing empty results.
test.setTimeout(180_000);
test.skip(
  process.env.WORKOS_KNOWLEDGE_OUTAGE_E2E !== "true",
  "run only while the owner gate has deliberately stopped Indexer",
);

test("an Indexer outage leaves project and Agent work usable", async ({ page }) => {
  const stamp = String(Date.now());

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Knowledge Outage ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Knowledge Outage");

  await page.getByLabel("Agent goal").fill("produce a review while the knowledge index is offline");
  await page.getByLabel("Markdown document").check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/).first()).toBeVisible({
    timeout: 120_000,
  });

  await page.getByTestId("open-knowledge-center").click();
  await page.getByTestId("knowledge-search-input").fill("deterministic synthetic output");
  await page.getByTestId("knowledge-search-submit").click();
  await expect(page.getByTestId("knowledge-unavailable")).toContainText(
    "Knowledge index is temporarily unavailable.",
  );
});
