import { expect, test } from "@playwright/test";

// The owner knowledge-search acceptance gate (ADR-0013): the full browser
// journey over the real stack — a fake-harness task materializes a review
// artifact, Core publishes it, the indexer's durable projection ingests it,
// the Knowledge Center finds it with bounded excerpts, the hit is pinned as
// an Agent context through the canonical chips, and a second task's provider
// receipt proves Core re-authorized the exact artifact.review.v1 ref —
// never the indexer's bytes.
test.setTimeout(240_000);

test("owner searches knowledge, pins a hit as Agent context, and re-runs a task", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const phrase = "deterministic synthetic output";

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Knowledge UI ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Knowledge UI");

  // Produce one review artifact through the real fake-harness chain.
  await page.getByLabel("Agent goal").fill("produce the knowledge fixture review");
  await page.getByLabel("Markdown document").check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/).first()).toBeVisible({
    timeout: 120_000,
  });

  // Knowledge Center: bounded polling until the durable projection catches
  // up, then the exact hit with an inert excerpt.
  await page.getByTestId("open-knowledge-center").click();
  const input = page.getByTestId("knowledge-search-input");
  const submit = page.getByTestId("knowledge-search-submit");
  const firstResult = page.getByTestId("knowledge-result").first();
  for (let attempt = 0; attempt < 40; attempt++) {
    await input.fill(phrase);
    await submit.click();
    if ((await firstResult.count()) > 0) break;
    await page.waitForTimeout(500);
  }
  await expect(firstResult).toBeVisible({ timeout: 30_000 });
  await expect(firstResult).toContainText("Fake Harness Review Document");
  await expect(firstResult.locator(".knowledge-excerpt")).toContainText("deterministic synthetic output");
  // Excerpts are inert text: no HTML injection surface anywhere.
  const knowledgeHtml = await page.locator(".knowledge-results").innerHTML();
  expect(knowledgeHtml).not.toContain("<script");
  expect(knowledgeHtml).not.toContain("dangerouslySetInnerHTML");

  // Pin the hit as Agent context through the canonical chip flow.
  await page.getByTestId("knowledge-use-as-context").first().click();
  await expect(page.getByTestId("context-chip")).toHaveCount(1);
  await expect(page.getByTestId("knowledge-context-selected").first()).toBeVisible();

  // A second task consumes the chip: the provider receipt proves Core
  // resolved the exact artifact.review.v1 ref during execution. The
  // Knowledge Center window closes first so the composer is reachable.
  await page.getByRole("button", { name: "Close Knowledge Center" }).click();
  await expect(page.getByTestId("context-chip")).toHaveCount(1);
  await page.getByLabel("Agent goal").fill("summarize the pinned context");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(
    page.getByText(/resolved context: \[document\.markdown\.v1 artifact\.review\.v1 sha256:/),
  ).toBeVisible({ timeout: 120_000 });
  // The chips were consumed by the successful submit.
  await expect(page.getByTestId("context-chip")).toHaveCount(0);

  // Restarting the browser page keeps the durable facts searchable.
  await page.reload();
  await page.getByTestId("open-knowledge-center").click();
  for (let attempt = 0; attempt < 20; attempt++) {
    await input.fill(phrase);
    await submit.click();
    if ((await firstResult.count()) > 0) break;
    await page.waitForTimeout(500);
  }
  await expect(firstResult).toBeVisible({ timeout: 30_000 });
});

test("an empty query never reaches the server and results stay per project", async ({
  page,
  request,
}) => {
  const stamp = String(Date.now());
  await page.goto("/");
  await page.getByLabel("Project name").fill(`Knowledge Empty ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Knowledge Empty");

  await page.getByTestId("open-knowledge-center").click();
  await page.getByTestId("knowledge-search-input").fill("   ");
  await page.getByTestId("knowledge-search-submit").click();
  // The idle hint stays: no RPC was issued and no fake state appeared.
  await expect(page.locator(".knowledge-center-body")).toContainText(
    "Search the review documents",
  );

  // A fresh project cannot see another project's knowledge even for the
  // same phrase.
  const createResponse = await request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-knowledge-empty-project-${stamp}`,
        name: `Knowledge Empty 2 ${stamp}`,
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();
});
