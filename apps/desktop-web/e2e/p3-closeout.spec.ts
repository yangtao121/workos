import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";
import { openDesktopApp } from "./open-app.js";

test("desktop versions dialog rolls published B back to A without mocked RPCs", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const dir = process.env.WORKOS_P3_GATE_DIR;
  test.skip(!dir, "run through the isolated P3 delivery gate");
  if (!dir) return;
  const fixture = JSON.parse(readFileSync(path.join(dir, "closeout-f02.json"), "utf8")) as {
    Project: string;
    ProjectName: string;
    Installation: string;
    App: string;
    CandidateVersion: string;
  };
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+k");
  const input = page.getByLabel("Search commands");
  await expect(input).toBeVisible();
  await input.fill(`Switch to project: ${fixture.ProjectName}`);
  await expect(
    page.getByRole("option").filter({
      hasText: new RegExp(`^Switch to project: ${fixture.ProjectName}`),
    }),
  ).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
    fixture.ProjectName,
  );
  await openDesktopApp(page, "app-library");
  const library = page.locator(".app-library");
  const row = library.locator(".app-row", { hasText: fixture.App });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  await expect(page.getByTestId("app-surface-frame")).toBeVisible({ timeout: 60_000 });
  await expect(
    page.frameLocator('[data-testid="app-surface-frame"]').getByText("P3-VALUE-42"),
  ).toBeVisible({ timeout: 60_000 });
  await expect(library).toHaveCount(0);
  await openDesktopApp(page, "app-library");
  await row.getByRole("button", { name: "Versions", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/pinned /)).toBeVisible();
  await expect(dialog.locator("strong").filter({ hasText: fixture.CandidateVersion })).toBeVisible({
    timeout: 20_000,
  });
  await dialog.getByRole("button", { name: /Roll back to / }).click();
  await expect(dialog.getByText(/Rolled back to /)).toBeVisible({ timeout: 30_000 });
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await row.getByRole("button", { name: "Open", exact: true }).click();
  await expect(
    page.frameLocator('[data-testid="app-surface-frame"]').getByText("P3-VALUE-0"),
  ).toBeVisible({ timeout: 60_000 });
});
