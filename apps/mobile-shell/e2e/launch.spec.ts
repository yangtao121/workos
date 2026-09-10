import { mkdir } from "node:fs/promises";
import { expect, test } from "@playwright/test";

// The launchable mobile entry (ADR-0019 W5): the built app mounts the
// shared shell with posture detection, the honest gateway state machine,
// and the device-key storage status. Runs against the static build served
// by the gate; no real backend is required for the honest-unavailable path.
test.setTimeout(60_000);

test("mounted shell renders posture and honest gateway state", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/workos.project.v1.ProjectService/ListProjects", (route) =>
    route.fulfill({ status: 503, contentType: "application/json", body: "{}" }),
  );
  await page.goto("/");
  const root = page.getByTestId("workos-mobile");
  await expect(root).toBeVisible();
  await expect(page.getByTestId("mobile-connection")).toContainText(
    /Connecting|Gateway unavailable/,
    { timeout: 30_000 },
  );
  await expect(page.getByTestId("mobile-unavailable")).toBeVisible();
  const posture = await page.getByTestId("mobile-posture").textContent();
  expect(["phone", "tablet", "foldable", "desktop"]).toContain(posture ?? "");
  await expect(page.getByTestId("mobile-key-status")).toContainText(/Device key:/);
  const capture = "../../docs/ui/mobile-shell/changes/20260910-mobile-worktree-integration/after";
  await mkdir(capture, { recursive: true });
  await page.screenshot({
    path: `${capture}/mobile-shell--unavailable--390x844.png`,
    animations: "disabled",
  });
});
