import { expect, test } from "@playwright/test";
import { readFileSync } from "node:fs";
import path from "node:path";

test("browser reads the real gateway Surface A, B, then restored A", async ({ page }) => {
  test.setTimeout(90_000);
  const dir = process.env.WORKOS_P3_GATE_DIR;
  test.skip(!dir, "run through the isolated P3 delivery gate");
  if (!dir) return;
  const fixture = JSON.parse(readFileSync(path.join(dir, "replay.json"), "utf8")) as {
    Project: string;
    Installation: string;
    CandidateVersion: string;
  };
  const rpc = async (service: string, method: string, data: unknown) => {
    const response = await page.request.post(`/${service}/${method}`, { data });
    expect(response.ok(), await response.text()).toBe(true);
    return response;
  };
  const projectRevision = async () => {
    const response = await rpc("workos.project.v1.ProjectService", "GetProject", {
      projectId: fixture.Project,
    });
    const body = (await response.json()) as { project: { revision: string } };
    return body.project.revision;
  };
  const surface = async (marker: string) => {
    const response = await rpc("workos.surface.v1.SurfaceService", "CreateSurface", {
      idempotencyKey: `p3-browser-${marker}-${String(Date.now())}`,
      projectId: fixture.Project,
      appInstanceId: fixture.Installation,
      deviceClass: "DEVICE_CLASS_DESKTOP",
      viewport: { width: 1280, height: 800, pixelRatio: 1 },
    });
    const body = (await response.json()) as { session: { url: string } };
    await page.goto(body.session.url);
    await expect(page.getByText(`P3-VALUE-${marker}`, { exact: true })).toBeVisible();
  };
  await surface("0");
  await rpc("workos.app.v1.AppInstallationService", "TransitionAppVersion", {
    idempotencyKey: "p3-browser-transition",
    projectId: fixture.Project,
    installationId: fixture.Installation,
    version: fixture.CandidateVersion,
    expectedProjectRevision: await projectRevision(),
  });
  await surface("42");
  await rpc("workos.app.v1.AppInstallationService", "RollbackAppVersion", {
    idempotencyKey: "p3-browser-rollback",
    projectId: fixture.Project,
    installationId: fixture.Installation,
    expectedProjectRevision: await projectRevision(),
  });
  await surface("0");
});
