import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";
import path from "node:path";

test.use({ viewport: { width: 1440, height: 900 } });
test.setTimeout(120_000);
test("a sandboxed SDK selects, reads and saves real workspace files with grant and etag checks", async ({
  page,
  context,
}) => {
  const fixture = process.env.WORKOS_FILES_FIXTURE;
  if (!fixture) {
    test.skip(true, "requires the explicit local workspace gate");
    return;
  }
  const { projectId } = JSON.parse(await readFile(fixture, "utf8")) as { projectId: string };
  const stamp = String(Date.now()),
    appId = `files-editor-${stamp}`;
  const html =
    '<!doctype html><html><head><meta charset="utf-8"><title>Workspace editor</title><style>body{font:14px system-ui;background:#121925;color:#edf1f8;padding:24px;margin:0}h1{font-size:21px}p{color:#a3afc2}button{padding:9px 13px;border:1px solid #40516b;border-radius:8px;background:#202c3e;color:#edf1f8;margin:0 6px 8px 0}output{display:block;padding:18px;background:#192334;border-radius:10px;margin-top:16px}</style></head><body><h1>Workspace editor</h1><p>Choose a document, then read or save it through WorkOS.</p><button id="pick">Choose file</button><button id="read">Read</button><button id="write">Save</button><button id="stale">Stale write</button><button id="escape">Path check</button><button id="symlink">Link check</button><output id="status">connecting</output><script src="app.js"></script></body></html>';
  const bundle = await page.request.post("/workos.artifact.v1.ArtifactService/CreateArtifact", {
    data: {
      idempotencyKey: `files-bundle-${stamp}`,
      artifact: { title: "Workspace editor fixture" },
      webBundle: {
        entrypoint: "index.html",
        files: [
          { path: "index.html", content: Buffer.from(html).toString("base64") },
          {
            path: "app.js",
            content: (await readFile("../../tmp/workos-files-fixture/app.js")).toString("base64"),
          },
        ],
      },
    },
  });
  expect(bundle.ok()).toBeTruthy();
  const { artifact } = (await bundle.json()) as { artifact: { id: string; digest: string } };
  const manifest = `apiVersion: workos.app/v1\nid: ${appId}\nname: Workspace editor\nversion: 1.0.0\nscope: project\nruntime:\n  type: web-bundle\n  artifactId: ${artifact.id}\n  artifactDigest: ${artifact.digest}\nsurfaces:\n  - id: main\n    renderer: web-bundle\n    route: /\n    adaptive: true\npermissions: [files.read, files.write]\nresources: {}\nhealth: {}\nmaintainer: {}\n`;
  const register = await page.request.post("/workos.app.v1.AppRegistryService/RegisterApp", {
    data: {
      idempotencyKey: `files-register-${stamp}`,
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
  await consent.getByRole("checkbox", { name: "files.read", exact: true }).check();
  await consent.getByRole("checkbox", { name: "files.write", exact: true }).check();
  await consent.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText(/Installed · pinned/)).toBeVisible();
  const capture = process.env.WORKOS_FILES_CAPTURE_DIR;
  const oldUI = process.env.WORKOS_FILES_BEFORE_DIR;
  if (oldUI && capture) {
    const before = await context.newPage();
    await before.route("**/*", async (route) => {
      const pathname = new URL(route.request().url()).pathname;
      if (pathname === "/")
        await route.fulfill({ path: path.join(oldUI, "index.html"), contentType: "text/html" });
      else if (/^\/assets\/[\w.-]+$/.test(pathname))
        await route.fulfill({ path: path.join(oldUI, pathname) });
      else await route.continue();
    });
    await before.goto("/");
    await before.getByTestId("open-app-library").click();
    await before
      .locator(".app-row", { hasText: appId })
      .getByRole("button", { name: "Open", exact: true })
      .click();
    const frame = before.frameLocator(".app-surface-frame");
    await expect(frame.locator("#status")).toHaveText("ready");
    await frame.locator("#pick").click();
    await expect(frame.locator("#status")).toHaveText("pick:error:permission_denied");
    await before
      .locator(".app-window")
      .screenshot({ path: `${capture}/before/files--picker--1440x900.png` });
    await before.getByRole("button", { name: "Close App", exact: true }).click();
    await before.close();
  }
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.frameLocator(".app-surface-frame");
  const status = frame.locator("#status");
  await expect(status).toHaveText("ready");
  await frame.locator("#pick").click();
  await expect(page.getByRole("dialog", { name: "Choose workspace files" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(status).toHaveText("pick:cancelled");
  await frame.locator("#pick").click();
  const picker = page.getByRole("dialog", { name: "Choose workspace files" });
  await picker.getByRole("button", { name: "Load more" }).click();
  await picker.getByRole("checkbox", { name: /notes.txt/ }).check();
  if (capture) await picker.screenshot({ path: `${capture}/after/files--picker--1440x900.png` });
  await picker.getByRole("button", { name: "Choose selected" }).click();
  await expect(status).toHaveText("pick:notes.txt");
  for (const [action, result] of [
    ["read", "Draft from fixture"],
    ["write", "saved"],
    ["read", "Saved in WorkOS"],
    ["stale", "error:aborted"],
    ["escape", "error:invalid_argument"],
    ["symlink", "error:permission_denied"],
  ] as const) {
    await frame.locator(`#${action}`).click();
    await expect(status).toHaveText(`${action}:${result}`);
  }
  const list = await page.request.post("/workos.app.v1.AppInstallationService/ListInstalledApps", {
    data: { projectId },
  });
  const { installations } = (await list.json()) as {
    installations: { id: string; appId: string }[];
  };
  const installation = installations.find((item) => item.appId === appId);
  if (!installation) throw new Error("fixture installation missing");
  const current = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
    data: { projectId },
  });
  const { project } = (await current.json()) as { project: { revision: string } };
  const revoke = await page.request.post("/workos.app.v1.AppInstallationService/SetAppGrants", {
    data: {
      idempotencyKey: `files-revoke-${stamp}`,
      projectId,
      installationId: installation.id,
      expectedProjectRevision: project.revision,
      grantedPermissions: [],
    },
  });
  expect(revoke.ok()).toBeTruthy();
  await frame.locator("#read").click();
  await expect(status).toHaveText("read:error:permission_denied");
  await frame.locator("#write").click();
  await expect(status).toHaveText("write:error:permission_denied");
});
