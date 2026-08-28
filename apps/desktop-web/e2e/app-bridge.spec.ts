import { expect, test } from "@playwright/test";

// The persistent acceptance volume accumulates registered apps across runs,
// so the catalog walk in the App Library spans many pages.
const libraryTimeout = 30_000;

test.setTimeout(180_000);

// The synthetic bundle implements the frame side of the versioned App Bridge
// protocol exactly like @workos/app-sdk: it acks the parent hello's nonce on
// the transferred port, then drives agent.run + agent.stream and renders the
// Fake Harness terminal summary — proving the result came through the real
// Gateway → runtime-host → Core → Harness chain, not any in-page fake.
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
        // protobuf-es projects the canonical oneof as { case, value }.
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

test("installs with explicit consent, bridges a project task, and revokes", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `e2e-app-bridge-${stamp}`;

  const html = `<!doctype html><title>Bridge E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-bridge-artifact-${stamp}`,
        artifact: { title: "Bridge E2E App" },
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
name: Bridge E2E App
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
        idempotencyKey: `e2e-bridge-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();

  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E Bridge ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E Bridge");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });

  // Install opens the consent dialog: the exact requested permissions, every
  // checkbox defaulting to unchecked — nothing is granted silently.
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    const checkbox = dialog.getByRole("checkbox", { name: capability });
    await expect(checkbox).not.toBeChecked();
    await checkbox.check();
  }

  // Confirming pins the exact displayed version with the selected grants.
  await page.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(row.getByText("Granted: agent.event.watch, agent.task.run")).toBeVisible({
    timeout: libraryTimeout,
  });

  // Open: the sandboxed iframe handshakes, then runs one real project task.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  expect(await frame.getAttribute("sandbox")).toBe("allow-scripts");
  expect(await frame.getAttribute("sandbox")).not.toContain("allow-same-origin");
  expect(await frame.getAttribute("referrerpolicy")).toBe("no-referrer");
  expect(await frame.getAttribute("src")).toMatch(/^\/surfaces\/[0-9a-f-]{36}\/$/);

  const frameText = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameText()).toHaveText("bridge-ready", { timeout: libraryTimeout });

  // The desktop DOM never carries the credential or its header name.
  const desktopHtml = await page.content();
  expect(desktopHtml).not.toContain("X-WorkOS-Bridge-Token");

  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  // The terminal summary embeds the task id minted by Core — a unique,
  // server-side product of the real chain.
  await expect(frameText()).toHaveText(/^terminal:Task [0-9a-f-]{36} completed by fake harness$/, {
    timeout: 90_000,
  });

  // Closing the window closes the session; the durable task facts stay.
  await page.getByRole("button", { name: "Close App", exact: true }).click();
  await expect(frame).toHaveCount(0);
});
