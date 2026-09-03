import { expect, test, type Page } from "@playwright/test";

// Deterministic visual capture for the ADR-0015 provider expansion: the
// Project harness settings surface listing the registered providers with
// their capability badges, including the new Codex and MCP server entries.
// Fixed fixture data only: no real user content, no credentials. Screenshots
// land in WORKOS_CAPTURE_DIR as the task's after/ evidence.
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";

test.setTimeout(240_000);

test.use({ viewport: { width: 1440, height: 900 } });

async function capture(page: Page, name: string) {
  if (!captureDir) return;
  await page.screenshot({ path: `${captureDir}/${name}`, fullPage: false });
}

const expanded = process.env.WORKOS_PROVIDERS_EXPANDED === "true";

test("captures harness settings with the expanded provider catalog", async ({ page }) => {
  test.skip(
    process.env.WORKOS_CODEX_FIXTURE_E2E !== "true",
    "requires the expanded provider fixture stack",
  );

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Provider Catalog ${String(Date.now())}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Provider Catalog");

  await page.getByRole("button", { name: "Project settings" }).click();
  const settings = page.locator(".harness-settings");
  await expect(
    settings.getByRole("radio", { name: "Select Deterministic Fake Harness" }),
  ).toBeVisible();
  if (expanded) {
    await expect(settings.getByRole("radio", { name: "Select Codex Harness" })).toBeVisible();
    await expect(settings.getByRole("radio", { name: "Select MCP Server Harness" })).toBeVisible();
    await expect(settings.getByText("codex · adapter")).toBeVisible();
    await expect(settings.getByText("mcp · adapter")).toBeVisible();
    await expect(settings.getByText("codex-auth.v1").first()).toBeVisible();
  } else {
    await expect(settings.getByRole("radio", { name: "Select Codex Harness" })).toHaveCount(0);
    await expect(settings.getByRole("radio", { name: "Select MCP Server Harness" })).toHaveCount(0);
  }

  const name = expanded
    ? "harness-settings--provider-catalog--1440x900.png"
    : "harness-settings--provider-catalog--1440x900.png";
  await capture(
    page,
    name.replace(
      "provider-catalog",
      expanded ? "provider-catalog-expanded" : "provider-catalog-baseline",
    ),
  );
  await expect(settings.getByText("Harness setting saved.")).toHaveCount(0);
});
