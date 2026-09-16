import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import path from "node:path";
import { desktopFixture } from "./desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";

// Render the actual application/component/CSS. Only RPC facts are fixtures;
// there is no duplicate HTML implementation of the dialog.
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const installation = {
  id: "01999999-9999-7999-8999-000000000003",
  projectId: "01999999-9999-7999-8999-000000000001",
  appId: "board-app",
  version: "1.1.0",
  manifestDigest: `sha256:${"a".repeat(64)}`,
  grantRevision: "1",
  grantedPermissions: [],
};
const states = [
  { file: "published", state: "published", digest: `sha256:${"ab".repeat(32)}` },
  { file: "canary-rollback", state: "rollback_pending", digest: `sha256:${"cd".repeat(32)}` },
  { file: "failed", state: "failed", digest: "" },
];
for (const viewport of [
  { width: 1440, height: 900 },
  { width: 820, height: 1180 },
  { width: 390, height: 844 },
]) {
  test(`real VersionDialog at ${String(viewport.width)}x${String(viewport.height)}`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await desktopFixture(page);
    const initialState = states[0];
    if (!initialState) throw new Error("missing release fixtures");
    let state = initialState;
    await page.route("**/workos.*/**", async (route) => {
      const method = new URL(route.request().url()).pathname.split("/").at(-1);
      const facts: Record<string, unknown> = {
        ListInstalledApps: { installations: [installation] },
        ListApps: { apps: [{ id: "board-app", name: "Board", version: "1.1.0" }] },
        ListAppVersionHistory: {
          snapshots: [
            { sequence: "1", version: "1.0.0", source: "install" },
            { sequence: "2", version: "1.1.0", source: "transition" },
          ],
        },
        GetReleaseStatus: {
          status: {
            installationId: installation.id,
            state: state.state,
            candidateArtifactDigest: state.digest,
          },
        },
      };
      if (!Object.hasOwn(facts, method ?? "")) {
        await route.fallback();
        return;
      }
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify(facts[method ?? ""]),
      });
    });
    await page.goto("/");
    await openDesktopApp(page, "app-library");
    for (const fixture of states) {
      state = fixture;
      await page.getByRole("button", { name: "Versions", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: /Versions/ });
      await expect(dialog.getByTestId("version-dialog-release")).toContainText(fixture.state);
      await expect(dialog.getByRole("button", { name: "Roll back to 1.0.0" })).toBeEnabled();
      if (captureDir) {
        mkdirSync(captureDir, { recursive: true });
        await page.screenshot({
          path: path.join(
            captureDir,
            `version-dialog--${fixture.file}--${String(viewport.width)}x${String(viewport.height)}.png`,
          ),
        });
      }
      await dialog.getByRole("button", { name: "Close", exact: true }).click();
    }
  });
}
