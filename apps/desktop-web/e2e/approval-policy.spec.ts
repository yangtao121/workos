import { expect, test, type Page } from "@playwright/test";

// The persistent acceptance volume accumulates registered apps across runs,
// so the catalog walk in the App Library spans many pages.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

// The synthetic bundle drives agent.run + agent.stream and renders the run
// lifecycle: waiting → approved → terminal. Every state string asserted below
// comes from the real Gateway → runtime-host → Core → Fake Harness chain.
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
      goal: 'Approval fixture goal.'
    });
    if (String(runResult.state) === '3') {
      root.textContent = 'waiting:approval-required';
    } else {
      root.textContent = 'state:' + String(runResult.state);
    }
    var summary = await new Promise(function (resolve, reject) {
      var decided = false;
      var entry = request('agent.stream', { taskId: runResult.taskId, afterSequence: '0' }, function (event) {
        var oneof = event && event.event;
        var value = oneof && (oneof.value !== undefined ? oneof.value : (oneof[oneof.case]));
        var name = oneof && oneof.case;
        if (name === 'approvalDecided') {
          decided = true;
          var decision = String(oneof.value && oneof.value.decision);
          root.textContent = decision === '1' || decision.indexOf('APPROVE') !== -1 ? 'approved:queued' : 'rejected:cancelled';
        }
        if (name === 'runCompleted') resolve(String(oneof.value.summary));
        if (name === 'runCancelled') resolve(decided ? 'task-cancelled-after-reject' : 'task-cancelled');
        if (name === 'runFailed') reject(new Error('run-failed'));
      });
      entry.then(function (value) { if (value && value.done) resolve('stream-done-without-terminal'); }, reject);
    });
    root.textContent = 'terminal:' + summary;
  } catch (error) {
    root.textContent = 'bridge-error:' + String(error.message);
  }
}
`;

async function registerApprovalApp(page: Page, stamp: string) {
  const appId = `e2e-app-approval-${stamp}`;
  const html = `<!doctype html><title>Approval E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-approval-artifact-${stamp}`,
        artifact: { title: "Approval E2E App" },
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
name: Approval E2E App
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
        idempotencyKey: `e2e-approval-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
  return appId;
}

test("require-approval policy gates app tasks until the owner decides", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = await registerApprovalApp(page, stamp);

  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E Approval ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E Approval");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const consent = page.getByRole("dialog");
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    await consent.getByRole("checkbox", { name: capability }).check();
  }
  await page.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // The installed row shows the versioned, finite system default.
  await expect(row.getByText(/Agent policy: system default \(allow\)/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // The policy editor flips the installation to require-approval.
  await row.getByRole("button", { name: "Agent policy" }).click();
  const dialog = page.getByRole("dialog", { name: "Agent policy" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText(/Current: system default/)).toBeVisible();
  await dialog.getByRole("radio", { name: /Require approval/ }).check();
  await dialog.getByRole("button", { name: "Save policy" }).click();
  await expect(dialog.getByText(/Agent policy saved/)).toBeVisible();
  await expect(row.getByText(/Agent policy: require approval · revision 1/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await dialog.getByRole("button", { name: "Close" }).click();

  // Open the app: the first run waits for the owner instead of queueing.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frameText = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameText()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  await expect(frameText()).toHaveText("waiting:approval-required", { timeout: libraryTimeout });

  // The owner approves inside the Agent Center window — the same durable task
  // continues in the app without any re-handshake. Raising the Agent Center
  // window by its header mirrors a real user bringing it to the front.
  await page.locator(".workos-window header", { hasText: "Agent Center" }).click();
  await page.getByRole("tab", { name: "Approvals" }).click();
  const approvals = page.getByLabel("Pending approvals");
  await expect(approvals.getByText(/Approval fixture goal|Approval fixture/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await approvals.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(page.getByText(/Task approved and queued/)).toBeVisible({ timeout: libraryTimeout });

  await expect(frameText()).toHaveText(/^terminal:Task [0-9a-f-]{36} completed by fake harness$/, {
    timeout: 90_000,
  });

  // The usage view separates the reserved allowance from reported usage.
  await page.getByRole("tab", { name: "Usage" }).click();
  const usage = page.getByLabel("Daily agent usage");
  await expect(usage.getByText(appId)).toBeVisible({ timeout: libraryTimeout });
  await expect(usage.getByText("1 tasks · 4096 output tokens")).toBeVisible({
    timeout: libraryTimeout,
  });
  await expect(usage.getByText(/1 tasks · .* in \/ .* out/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // A second run under the same policy is rejected and never executes.
  await page.frameLocator(".app-surface-frame").locator("#run-task").click();
  await expect(frameText()).toHaveText("waiting:approval-required", { timeout: libraryTimeout });
  await page.getByRole("tab", { name: "Approvals" }).click();
  const pending = page.getByLabel("Pending approvals");
  await pending.getByRole("button", { name: "Reject", exact: true }).click();
  await expect(page.getByText(/Task rejected/)).toBeVisible({ timeout: libraryTimeout });
  await expect(frameText()).toHaveText("terminal:task-cancelled-after-reject", {
    timeout: 90_000,
  });
});
