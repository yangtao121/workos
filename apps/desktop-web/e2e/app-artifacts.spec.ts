import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";
import path from "node:path";

test.use({ viewport: { width: 1440, height: 900 } });
test.setTimeout(120_000);
test("an installed SDK creates a durable review artifact and opens its own project viewer", async ({
  page,
  context,
}) => {
  test.skip(!process.env.WORKOS_APP_ARTIFACTS, "requires built SDK fixture");
  const stamp = String(Date.now()),
    appId = `artifact-author-${stamp}`;
  const projectResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    { data: { name: "Writing workspace", idempotencyKey: `artifact-project-${stamp}` } },
  );
  expect(projectResponse.ok()).toBeTruthy();
  const { project } = (await projectResponse.json()) as { project: { id: string } };
  const projectId = project.id;
  const html =
    '<!doctype html><html><head><meta charset="utf-8"><title>Document author</title><style>body{font:14px system-ui;background:#121925;color:#edf1f8;padding:24px;margin:0}h1{font-size:21px}p{color:#a3afc2}button{padding:9px 13px;border:1px solid #40516b;border-radius:8px;background:#202c3e;color:#edf1f8;margin:0 6px 8px 0}output{display:block;padding:18px;background:#192334;border-radius:10px;margin-top:16px}</style></head><body><h1>Document author</h1><p>Save release notes to your project and open them for review.</p><button id="create">Save document</button><button id="open">Open document</button><button id="conflict">Conflict check</button><button id="foreign">Project check</button><output>connecting</output><script src="app.js"></script></body></html>';
  const bundle = await page.request.post("/workos.artifact.v1.ArtifactService/CreateArtifact", {
    data: {
      idempotencyKey: `artifact-bundle-${stamp}`,
      artifact: { title: "Document author fixture" },
      webBundle: {
        entrypoint: "index.html",
        files: [
          { path: "index.html", content: Buffer.from(html).toString("base64") },
          {
            path: "app.js",
            content: (await readFile("../../tmp/workos-artifacts-fixture/app.js")).toString(
              "base64",
            ),
          },
        ],
      },
    },
  });
  expect(bundle.ok()).toBeTruthy();
  const { artifact } = (await bundle.json()) as { artifact: { id: string; digest: string } };
  const manifest = `apiVersion: workos.app/v1\nid: ${appId}\nname: Document author\nversion: 1.0.0\nscope: project\nruntime:\n  type: web-bundle\n  artifactId: ${artifact.id}\n  artifactDigest: ${artifact.digest}\nsurfaces:\n  - id: main\n    renderer: web-bundle\n    route: /\n    adaptive: true\npermissions: [artifact.read, artifact.write]\nresources: {}\nhealth: {}\nmaintainer: {}\n`;
  const register = await page.request.post("/workos.app.v1.AppRegistryService/RegisterApp", {
    data: {
      idempotencyKey: `artifact-register-${stamp}`,
      manifestYaml: Buffer.from(manifest).toString("base64"),
    },
  });
  expect(register.ok()).toBeTruthy();
  await context.addInitScript((id) => {
    sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");
  await page.getByTestId("open-app-library").click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const consent = page.getByRole("dialog");
  for (const name of ["artifact.read", "artifact.write"])
    await consent.getByRole("checkbox", { name, exact: true }).check();
  await consent.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText(/Installed · pinned/)).toBeVisible();
  const capture = process.env.WORKOS_ARTIFACTS_CAPTURE_DIR,
    baseline = process.env.WORKOS_ARTIFACTS_BEFORE_DIR;
  if (capture && baseline) {
    const before = await context.newPage();
    await before.route("**/*", async (route) => {
      const pathname = new URL(route.request().url()).pathname;
      if (pathname === "/")
        await route.fulfill({ path: path.join(baseline, "index.html"), contentType: "text/html" });
      else if (/^\/assets\/[\w.-]+$/.test(pathname))
        await route.fulfill({ path: path.join(baseline, pathname) });
      else await route.continue();
    });
    await before.goto("/");
    await before.getByTestId("open-app-library").click();
    await before
      .locator(".app-row", { hasText: appId })
      .getByRole("button", { name: "Open", exact: true })
      .click();
    const frame = before.frameLocator(".app-surface-frame");
    await expect(frame.locator("output")).toHaveText("ready");
    await frame.locator("#create").click();
    await expect(frame.locator("output")).toHaveText("create:error:permission_denied");
    await before
      .locator(".app-window")
      .screenshot({ path: `${capture}/before/artifacts--app-create--1440x900.png` });
    await before.getByRole("button", { name: "Close App", exact: true }).click();
    await before.close();
  }
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.frameLocator(".app-surface-frame"),
    output = frame.locator("output");
  await expect(output).toHaveText("ready");
  await frame.locator("#create").click();
  await expect(output).toHaveText("create:saved");
  const id = await output.getAttribute("data-artifact-id");
  expect(id).toBeTruthy();
  await frame.locator("#create").click();
  await expect(output).toHaveAttribute("data-artifact-id", id ?? "");
  await frame.locator("#conflict").click();
  await expect(output).toHaveText("conflict:error:aborted");
  if (capture)
    await page
      .locator(".app-window")
      .screenshot({ path: `${capture}/after/artifacts--app-create--1440x900.png` });
  await frame.locator("body").evaluate((body, id) => {
    body.dataset.foreignId = id;
  }, artifact.id);
  await frame.locator("#foreign").click();
  await expect(output).toHaveText("foreign:error:not_found");
  await frame.locator("#open").click();
  await expect(page.locator(".artifact-viewer-body")).toContainText("Created by the project app.");
  const content = await page.request.post("/workos.artifact.v1.ArtifactService/GetReviewArtifact", {
    data: { artifactId: id },
  });
  expect(content.ok()).toBeTruthy();
  const fact = (await content.json()) as {
    artifact: { sourceAppInstanceId: string; sourceTaskId?: string; projectId: string };
  };
  expect(fact.artifact.projectId).toBe(projectId);
  expect(fact.artifact.sourceTaskId ?? "").toBe("");
  expect(fact.artifact.sourceAppInstanceId).toBeTruthy();
  if (capture)
    await page
      .locator(".artifact-viewer-body")
      .screenshot({ path: `${capture}/after/artifacts--app-review--1440x900.png` });
  await page.getByRole("button", { name: "Close Artifact Review", exact: true }).click();
  const current = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
    data: { projectId },
  });
  const { project: latest } = (await current.json()) as { project: { revision: string } };
  const revoke = await page.request.post("/workos.app.v1.AppInstallationService/SetAppGrants", {
    data: {
      idempotencyKey: `artifact-revoke-${stamp}`,
      projectId,
      installationId: fact.artifact.sourceAppInstanceId,
      expectedProjectRevision: latest.revision,
      grantedPermissions: [],
    },
  });
  expect(revoke.ok()).toBeTruthy();
  await frame.locator("#create").click();
  await expect(output).toHaveText("create:error:permission_denied");
  await frame.locator("#open").click();
  await expect(output).toHaveText("open:error:permission_denied");
  expect(
    (
      await page.request.post("/workos.artifact.v1.AppArtifactService/CreateAppReviewArtifact", {
        data: {},
      })
    ).status(),
  ).toBe(404);
});
