import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
const directory = process.env.WORKOS_SERVICE_CAPTURE_DIR;
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`knowledge and telemetry ${String(width)}x${String(height)}`, async ({ page }) => {
    if (!directory) {
      test.skip(true, "explicit service view capture");
      return;
    }
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    const digest = `sha256:${"a".repeat(64)}`;
    await page.route("**/SearchHybrid", async (route) => {
      const hits = [
        {
          id: "01999999-9999-7999-8999-000000000011",
          type: "artifact.review.v1",
          artifactType: "document.markdown.v1",
          title: "Release plan",
          excerpt: "Decisions and review notes for the next release.",
        },
        {
          id: "01999999-9999-7999-8999-000000000012",
          type: "workspace.file.v1",
          artifactType: "workspace.text.v1",
          title: "docs/release-checklist.md",
          excerpt: "Validate the build, review changes, and prepare the release.",
        },
      ].map((hit) => ({
        artifactId: hit.id,
        artifactType: hit.artifactType,
        digest,
        contextRef: `${hit.type}:${hit.id}:${digest}`,
        sourceRef: { type: hit.type, id: hit.id, revision: digest },
        title: hit.title,
        excerpt: hit.excerpt,
        score: 0.8,
      }));
      await route.fulfill({ json: { hits, freshness: { caughtUp: true } } });
    });
    let offline = false;
    await page.route("**/GetTelemetrySummary", async (route) => {
      if (offline)
        await route.fulfill({
          status: 503,
          json: { code: "unavailable", message: "fixture offline" },
        });
      else
        await route.fulfill({
          json: {
            spansObserved: "128",
            attributesDropped: "2",
            services: [
              {
                service: "workos-core",
                spanCount: "80",
                errorCount: "1",
                avgDurationMs: 12.4,
                maxDurationMs: 45.8,
                attributesDropped: "2",
              },
              {
                service: "runtime-host",
                spanCount: "48",
                errorCount: "0",
                avgDurationMs: 8.2,
                maxDurationMs: 24.1,
                attributesDropped: "0",
              },
            ],
          },
        });
    });
    await page.goto("/");
    await expect(
      page.getByRole("button", { name: "Notifications", exact: true }).first(),
    ).toBeVisible();
    async function open(label: string) {
      await page.keyboard.press("ControlOrMeta+k");
      await page.getByLabel("Search commands").fill(label);
      await page
        .getByRole("dialog", { name: "Command palette" })
        .getByRole("button", { name: new RegExp(`^${label}`) })
        .click();
    }
    await open("Open Knowledge Center");
    await page.getByLabel("Search project knowledge").fill("release");
    await page.getByTestId("knowledge-search-submit").click();
    await expect(page.getByRole("list", { name: "Knowledge results" })).toContainText(
      "Release plan",
    );
    await page.screenshot({
      path: `${directory}/knowledge--mixed-results--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
    await open("Open System Monitor");
    await expect(page.getByRole("table")).toContainText("workos-core");
    await page.screenshot({
      path: `${directory}/monitor--telemetry--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
    offline = true;
    await page.getByRole("button", { name: "Refresh telemetry" }).click();
    if (!process.env.WORKOS_VISUAL_BASELINE)
      await expect(page.getByText("Telemetry is temporarily unavailable.")).toBeVisible();
    else await expect(page.getByRole("table")).toHaveCount(0);
    await page.screenshot({
      path: `${directory}/monitor--telemetry-unavailable--${String(width)}x${String(height)}.png`,
      animations: "disabled",
    });
  });
}
