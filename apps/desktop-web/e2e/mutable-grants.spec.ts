import { expect, test } from "@playwright/test";

// The persistent acceptance volume accumulates registered apps across runs,
// so the catalog walk in the App Library spans many pages.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

// The synthetic bundle is the same trusted-host bridge fixture as the app
// bridge E2E: it acks the parent hello on the transferred port, drives
// agent.run + agent.stream, and records either the Fake Harness terminal
// summary or the bridge error code — so every outcome visible in the frame
// came through the real Gateway → runtime-host → Core → Harness chain.
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
  root.textContent = 'bridge-ready';
  port.onmessage = function (message) {
    var envelope = message.data;
    var entry = pending.get(envelope.requestId);
    if (!entry) return;
    if (envelope.type === 'event') {
      if (entry.onEvent) entry.onEvent(envelope.payload);
      return;
    }
    pending.delete(envelope.requestId);
    if (envelope.type === 'response') entry.resolve(envelope.payload);
    else if (envelope.type === 'error') entry.reject(new Error(String(envelope.code)));
  };
  var button = document.createElement('button');
  button.id = 'run-task';
  button.textContent = 'Run project task';
  button.addEventListener('click', function () { void run(); });
  document.body.appendChild(button);
});
function request(method, payload, onEvent) {
  return new Promise(function (resolve, reject) {
    var requestId = 'req-' + String(++seq);
    pending.set(requestId, { resolve: resolve, reject: reject, onEvent: onEvent });
    port.postMessage({ version: 'workos.app-bridge/v1', type: 'request', requestId: requestId, method: method, payload: payload });
  });
}
async function run() {
  try {
    var runResult = await request('agent.run', {
      idempotencyKey: 'e2e-' + String(Date.now()) + '-' + String(Math.floor(Math.random() * 1e9)),
      role: '',
      goal: 'Describe the fixture project.'
    });
    var summary = await new Promise(function (resolve, reject) {
      var entry = request('agent.stream', { taskId: runResult.taskId, afterSequence: '0' }, function (event) {
        var oneof = event && event.event;
        var completed = oneof && (oneof.case === 'runCompleted' ? oneof.value : oneof.runCompleted);
        var failed = oneof && (oneof.case === 'runFailed' ? oneof.value : oneof.runFailed);
        if (completed) resolve(String(completed.summary));
        if (failed) reject(new Error('run-failed'));
      });
      entry.then(function (value) { if (value && value.done) resolve('stream-done-without-terminal'); }, reject);
    });
    root.textContent = 'terminal:' + summary;
  } catch (error) {
    root.textContent = 'bridge-error:' + String(error.message);
  }
}
`;

// Mutable grant lifecycle through the real Gateway → Core → PostgreSQL chain:
// install with grants, open a surface, get a Fake Harness terminal event,
// revoke everything from the Manage-permissions dialog (from a second tab of
// the same browser identity, so the first tab's window survives), prove the
// old surface's bridge call is denied server-side — not hidden by the UI —
// then re-grant and reopen to see the new epoch work end to end.
test("revokes and re-grants app permissions without reinstalling", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `e2e-mutable-grants-${stamp}`;

  const html = `<!doctype html><title>Mutable Grants E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-grants-artifact-${stamp}`,
        artifact: { title: "Mutable Grants E2E App" },
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
  const artifact = (await artifactResponse.json()) as {
    artifact: { id: string; digest: string };
  };

  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Mutable Grants E2E App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: ${artifact.artifact.id}
  artifactDigest: ${artifact.artifact.digest}
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [agent.task.run, agent.event.watch]
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `e2e-grants-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();

  // Tab 1: install with both requested permissions explicitly granted.
  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E Grants ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E Grants");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const consent = page.getByRole("dialog");
  await expect(consent).toBeVisible({ timeout: libraryTimeout });
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    await consent.getByRole("checkbox", { name: capability }).check();
  }
  await page.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText("Granted: agent.event.watch, agent.task.run")).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(row.getByText(/grant revision 1/)).toBeVisible({ timeout: libraryTimeout });

  // Open the surface and drive one real agent task through the Fake Harness.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  const frameText = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameText()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  await expect(frameText()).toHaveText(/^terminal:Task [0-9a-f-]{36} completed by fake harness$/, {
    timeout: 90_000,
  });

  // Tab 2 (same browser identity, so the same owner): revoke every
  // permission from the Manage-permissions dialog. The dialog edits the exact
  // pinned version and starts from the current grant. A new tab starts with
  // its own sessionStorage and the sidebar lists only the first project
  // page, so the card of this run's project need not be rendered at all;
  // seeding the app's own persisted-selection key lands the tab on the
  // project directly (the desktop resolves a stored id via GetProject even
  // beyond the first page — its supported reload path).
  const activeProjectId = await page.evaluate(() =>
    window.sessionStorage.getItem("workos.activeProjectId"),
  );
  expect(activeProjectId).toBeTruthy();
  const pageTwo = await page.context().newPage();
  await pageTwo.addInitScript((id: string | null) => {
    if (id !== null) {
      window.sessionStorage.setItem("workos.activeProjectId", id);
    }
  }, activeProjectId);
  await pageTwo.goto("/");
  await expect(pageTwo.locator(".project-switcher")).toContainText(`E2E Grants ${stamp}`, {
    timeout: libraryTimeout,
  });
  await pageTwo.getByRole("button", { name: "App Library" }).click();
  const rowTwo = pageTwo.locator(".app-library .app-row", { hasText: appId });
  await expect(rowTwo.getByText(/grant revision 1/)).toBeVisible({ timeout: libraryTimeout });
  await rowTwo.getByRole("button", { name: "Manage permissions" }).click();
  const manage = pageTwo.getByRole("dialog");
  await expect(manage).toBeVisible({ timeout: libraryTimeout });
  await expect(manage.getByText(/pinned version 1\.0\.0/)).toBeVisible();
  await expect(manage.getByText(/Current grant revision 1/)).toBeVisible();
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    const checkbox = manage.getByRole("checkbox", { name: capability });
    await expect(checkbox).toBeChecked();
    await checkbox.uncheck();
  }
  await expect(
    manage.getByText("Saving with nothing selected revokes every permission."),
  ).toBeVisible();
  await manage.getByRole("button", { name: "Revoke all permissions" }).click();
  await expect(
    manage.getByText("Permissions saved. Reopen the app for the new permissions to take effect."),
  ).toBeVisible({ timeout: libraryTimeout });
  await expect(rowTwo.getByText("Granted: none")).toBeVisible({ timeout: libraryTimeout });
  await expect(rowTwo.getByText(/grant revision 2/)).toBeVisible({ timeout: libraryTimeout });
  // No window of this installation existed in tab 2, so nothing closed here.
  await expect(pageTwo.locator(".app-surface-frame")).toHaveCount(0);

  // Tab 1 still holds the pre-revocation surface: its Run button is still in
  // the DOM (the UI hid nothing), but the same bridge call that produced a
  // terminal event before is now denied server-side at the grant revision
  // check — proving the old epoch is invalid, not merely dismissed.
  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  await expect(frameText()).toHaveText("bridge-error:permission_denied", {
    timeout: libraryTimeout,
  });

  // The desktop DOM never carries the bridge credential or its header name.
  expect((await page.content()).includes("X-WorkOS-Bridge-Token")).toBe(false);

  // Close the stale window and re-grant both permissions from tab 1: the
  // dialog starts from the now-empty current grant at revision 2.
  await page.getByRole("button", { name: "Close App", exact: true }).click();
  await expect(frame).toHaveCount(0);
  await row.getByRole("button", { name: "Manage permissions" }).click();
  const manageAgain = page.getByRole("dialog");
  await expect(manageAgain).toBeVisible({ timeout: libraryTimeout });
  await expect(manageAgain.getByText(/Current grant revision 2/)).toBeVisible();
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    await expect(manageAgain.getByRole("checkbox", { name: capability })).not.toBeChecked();
    await manageAgain.getByRole("checkbox", { name: capability }).check();
  }
  await expect(manageAgain.getByText("Adding agent.task.run")).toBeVisible();
  await expect(manageAgain.getByText("Adding agent.event.watch")).toBeVisible();
  await manageAgain.getByRole("button", { name: "Save permissions" }).click();
  await expect(
    manageAgain.getByText(
      "Permissions saved. Reopen the app for the new permissions to take effect.",
    ),
  ).toBeVisible({ timeout: libraryTimeout });
  await expect(row.getByText("Granted: agent.event.watch, agent.task.run")).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(row.getByText(/grant revision 3/)).toBeVisible({ timeout: libraryTimeout });
  // Dismiss the saved dialog before reopening the surface.
  await manageAgain.getByRole("button", { name: "Close" }).click();
  await expect(manageAgain).not.toBeVisible();

  // Reopen: the fresh surface carries the new grant epoch, and the very same
  // bridge flow reaches a Fake Harness terminal event again.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const reopened = page.locator(".app-surface-frame");
  await expect(reopened).toBeVisible({ timeout: libraryTimeout });
  await expect(page.frameLocator(".app-surface-frame").locator("#root")).toHaveText(
    "bridge-ready",
    {
      timeout: libraryTimeout,
    },
  );
  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  await expect(page.frameLocator(".app-surface-frame").locator("#root")).toHaveText(
    /^terminal:Task [0-9a-f-]{36} completed by fake harness$/,
    { timeout: 90_000 },
  );

  await pageTwo.close();
});
