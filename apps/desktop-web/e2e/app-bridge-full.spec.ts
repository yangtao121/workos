import { openDesktopApp } from "./open-app.js";
import { expect, test, type Page } from "@playwright/test";

// The full App Bridge capability gate (ADR-0016 §5): a granted web bundle
// exercises the shell-side bridge methods — project.current (project.read
// grant), theme.get, window.setTitle, window.close — plus the negative
// path for an ungranted capability. Everything runs through the real
// chain: opaque-origin iframe → app-host shell dispatch / Gateway →
// runtime-host → Core re-authorization.
const libraryTimeout = 30_000;

test.setTimeout(240_000);
test.use({ viewport: { width: 1440, height: 900 } });
const baseline = !!process.env.WORKOS_VISUAL_BASELINE;
const capture = process.env.WORKOS_BRIDGE_CAPTURE_DIR;

const fixtureScript = `
var root = document.getElementById('root');
root.textContent = 'bridge-pending';
var port = null; var seq = 0; var pending = new Map();
window.addEventListener('message', function (event) {
  if (event.source !== window.parent) return;
  var hello = event.data;
  if (!hello || hello.version !== 'workos.app-bridge/v1' || hello.type !== 'hello') return;
  if (!Array.isArray(event.ports) || event.ports.length !== 1) return;
  port = event.ports[0];
  port.start();
  port.postMessage({ version: hello.version, type: 'ack', nonce: hello.nonce });
  var methods = document.createElement('p');
  methods.id = 'methods';
  methods.textContent = 'methods:' + (hello.methods || []).sort().join(',');
  document.body.appendChild(methods);
  var out = document.createElement('p');
  out.id = 'out'; out.setAttribute('aria-label', 'Bridge result');
  document.body.appendChild(out);
  root.textContent = 'bridge-ready';
  port.onmessage = function (message) {
    var envelope = message.data;
    var entry = pending.get(envelope.requestId);
    if (!entry) return;
    pending.delete(envelope.requestId);
    if (envelope.type === 'response') entry.resolve(envelope.payload);
    else if (envelope.type === 'error') entry.reject(new Error(String(envelope.code)));
  };
});
function request(method, payload) {
  return new Promise(function (resolve, reject) {
    var requestId = 'req-' + String(++seq);
    pending.set(requestId, { resolve: resolve, reject: reject });
    port.postMessage({ version: 'workos.app-bridge/v1', type: 'request', requestId: requestId, method: method, payload: payload });
  });
}
async function run(method, payload, label) {
  var out = document.getElementById('out');
  try {
    var result = await request(method, payload);
    var keys = Object.keys(result).sort().join(',');
    out.textContent = label + '-ok:' + keys + ':' + (result.name || result.scheme || result.projectId || '');
  } catch (error) {
    out.textContent = label + '-error:' + String(error.message).replace(/[^a-z_]/g, '');
  }
}
document.addEventListener('DOMContentLoaded', function () {
  function button(id, label, method, payload, tag) {
    var el = document.createElement('button');
    el.id = id; el.textContent = label;
    document.body.appendChild(el);
    el.addEventListener('click', function () { void run(method, payload, tag); });
  }
  button('project', 'Project current', 'project.current', {}, 'project');
  button('theme', 'Theme get', 'theme.get', {}, 'theme');
  button('knowledge', 'Knowledge search', 'knowledge.search', { query: 'probe' }, 'knowledge');
  button('rename', 'Rename window', 'window.setTitle', { title: 'Renamed E2E' }, 'rename');
  button('badge', 'Set badge', 'window.setBadge', { count: 3 }, 'badge');
  button('maximize', 'Maximize', 'window.maximize', {}, 'maximize');
  button('minimize', 'Minimize', 'window.minimize', {}, 'minimize');
  button('closewin', 'Close window', 'window.close', {}, 'close');
});
`;

async function createBundle(page: Page, stamp: string) {
  const html = `<!doctype html><title>Bridge Full E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-app-bridge-full-artifact-${stamp}`,
        artifact: { title: `Bridge Full E2E App` },
        webBundle: {
          entrypoint: "index.html",
          files: [
            { path: "index.html", content: btoa(html) },
            { path: "app.js", content: btoa(fixtureScript) },
          ],
        },
      },
    },
  );
  expect(artifactResponse.ok()).toBeTruthy();
  return (await artifactResponse.json()) as {
    artifact: { id: string; digest: string };
  };
}

async function registerApp(
  page: Page,
  stamp: string,
  appId: string,
  artifact: { id: string; digest: string },
  permissions: string,
) {
  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Bridge Full E2E App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: ${artifact.id}
  artifactDigest: ${artifact.digest}
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [${permissions}]
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `e2e-app-bridge-full-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("shell-side bridge methods exercise project.current, theme, window management; ungranted capability fails closed", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-app-bridge-full-${stamp}`;

  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-app-bridge-full-project-${stamp}`,
        name: "Bridge workspace",
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();
  const created = (await createResponse.json()) as { project: { id: string; name: string } };
  const projectId = created.project.id;

  const artifact = await createBundle(page, stamp);
  await registerApp(page, stamp, appId, artifact.artifact, "project.read");

  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");

  await openDesktopApp(page, "app-library");
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("checkbox", { name: "project.read" }).check();
  await page.getByRole("button", { name: "Install with 1 permission" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  const frameRoot = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameRoot()).toHaveText("bridge-ready", { timeout: libraryTimeout });

  if (baseline) await page.getByRole("button", { name: "Close App Library", exact: true }).click();
  else await expect(page.locator(".app-library")).toHaveCount(0);

  // Shell methods are negotiated: project.current from the project.read
  // grant; theme/window management as inherent shell-side surface facts.
  const advertised = page.frameLocator(".app-surface-frame").locator("#methods");
  await expect(advertised).toHaveText(
    /methods:.*project\.current.*theme\.get.*window\.close.*window\.setTitle/,
    { timeout: libraryTimeout },
  );

  const out = page.frameLocator(".app-surface-frame").locator("#out");

  // project.current: real project facts through the granted bridge method.
  const frameLoc = () => page.frameLocator(".app-surface-frame");

  await frameLoc().locator("#project").click();
  await expect(out).toHaveText(/project-ok:name,projectId,revision/, { timeout: 30_000 });

  // theme.get: the shell's active scheme.
  await frameLoc().locator("#theme").click();
  await expect(out).toHaveText(/theme-ok:scheme:dark/, { timeout: 30_000 });

  // Ungranted capability fails closed with zero side effects.
  await frameLoc().locator("#knowledge").click();
  await expect(out).toHaveText("knowledge-error:permission_denied", { timeout: 30_000 });

  // window.setTitle renames the hosting window in the desktop shell.
  await frameLoc().locator("#rename").click();
  await expect(page.locator("strong").filter({ hasText: "Renamed E2E" }).first()).toBeVisible({
    timeout: 30_000,
  });

  if (!baseline) {
    await frameLoc().locator("#badge").click();
    await expect(page.getByLabel("App badge 3", { exact: true })).toBeVisible();
    await frameLoc().locator("#maximize").click();
    await expect(page.locator(".app-window")).toHaveAttribute("data-mode", "maximized");
    await frameLoc().locator("#minimize").click();
    await expect(page.locator(".app-window")).toBeHidden();
    await page.locator(".dock").getByRole("button", { name: "Open Renamed E2E" }).click();
    await expect(page.locator(".app-window")).toHaveAttribute("data-mode", "maximized");
    await page.getByRole("button", { name: "Restore Renamed E2E", exact: true }).click();
  }
  if (capture)
    await page
      .locator(".app-window")
      .screenshot({ path: `${capture}/app-bridge--window--1440x900.png`, animations: "disabled" });
  // The ack is delivered before the host closes the surface.
  await frameLoc().locator("#closewin").click();
  await expect(frame).toBeHidden({ timeout: 30_000 });
  if (baseline) return;

  // An installation mutation outside this page leaves the old iframe alive.
  // Every shell operation must still re-authorize against current Core facts.
  await page.getByTestId("open-app-library").click();
  await row.getByRole("button", { name: "Open", exact: true }).click();
  await expect(frameRoot()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  const installed = await page.request.post(
    "/workos.app.v1.AppInstallationService/ListInstalledApps",
    { data: { projectId } },
  );
  const installations = (await installed.json()) as {
    installations: { id: string; appId: string }[];
  };
  const installation = installations.installations.find((item) => item.appId === appId);
  if (!installation) throw new Error("Fixture installation missing");
  const current = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
    data: { projectId },
  });
  const { project } = (await current.json()) as { project: { revision: string } };
  const revoke = await page.request.post("/workos.app.v1.AppInstallationService/SetAppGrants", {
    data: {
      idempotencyKey: `revoke-${stamp}`,
      projectId,
      installationId: installation.id,
      expectedProjectRevision: project.revision,
      grantedPermissions: [],
    },
  });
  expect(revoke.ok()).toBeTruthy();
  for (const [button, tag] of [
    ["project", "project"],
    ["rename", "rename"],
    ["closewin", "close"],
  ] as const) {
    await frameLoc().locator(`#${button}`).click();
    await expect(out).toHaveText(`${tag}-error:permission_denied`, { timeout: 30_000 });
  }
  await expect(frame).toBeVisible();
});
