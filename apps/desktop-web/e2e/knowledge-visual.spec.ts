import { expect, test, type Page } from "@playwright/test";

// Deterministic visual capture for the project knowledge-search slice
// (ADR-0013). Fixed fixture data only: no real user content, no random
// identifiers in frame, no credentials. Screenshots land in
// WORKOS_CAPTURE_DIR as the task's after/ evidence and in current/.
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const libraryTimeout = 30_000;

test.setTimeout(240_000);

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
  var input = document.createElement('input');
  input.id = 'query'; input.setAttribute('aria-label', 'App knowledge query');
  document.body.appendChild(input);
  var button = document.createElement('button');
  button.id = 'search'; button.textContent = 'Search project knowledge';
  document.body.appendChild(button);
  var out = document.createElement('ul');
  out.id = 'results';
  document.body.appendChild(out);
  button.addEventListener('click', function () { void search(input.value); });
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
async function search(query) {
  var out = document.getElementById('results');
  out.textContent = '';
  var result = await request('knowledge.search', { query: query, pageSize: 20, pageToken: '' });
  (result.hits || []).forEach(function (hit) {
    var li = document.createElement('li');
    var title = document.createElement('strong'); title.textContent = hit.title;
    var excerpt = document.createElement('span'); excerpt.textContent = hit.excerpt;
    li.appendChild(title); li.appendChild(excerpt);
    out.appendChild(li);
  });
}
`;

async function capture(page: Page, name: string) {
  if (!captureDir) return;
  await page.screenshot({ path: `${captureDir}/${name}`, fullPage: false });
}

async function stabilizeEvidenceFrame(page: Page) {
  await page.addStyleTag({
    content: `
      .mission-control .project-card:not(.active):not(.new-project) { display: none !important; }
      .task-snapshot dd:last-of-type { visibility: hidden !important; }
    `,
  });
}

async function registerKnowledgeApp(page: Page, stamp: string, appId: string) {
  const html = `<!doctype html><title>Knowledge App</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `visual-knowledge-artifact-${stamp}`,
        artifact: { title: "Knowledge Visual App" },
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
name: Knowledge Visual App
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
permissions: [knowledge.read]
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `visual-knowledge-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("captures knowledge center and app surface evidence", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `visual-knowledge-${stamp}`;
  const phrase = "deterministic synthetic output";

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await stabilizeEvidenceFrame(page);
  await page.getByLabel("Project name").fill("Knowledge Lab");
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Knowledge Lab");
  await page.getByLabel("Agent goal").fill("produce the knowledge fixture review");
  await page.getByLabel("Markdown document").check();
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText(/completed by fake harness/).first()).toBeVisible({
    timeout: 120_000,
  });

  // Expanded: Knowledge Center results.
  await page.getByTestId("open-knowledge-center").click();
  const input = page.getByTestId("knowledge-search-input");
  const firstResult = page.getByTestId("knowledge-result").first();
  for (let attempt = 0; attempt < 40; attempt++) {
    await input.fill(phrase);
    await page.getByTestId("knowledge-search-submit").click();
    if ((await firstResult.count()) > 0) break;
    await page.waitForTimeout(500);
  }
  await expect(firstResult).toBeVisible({ timeout: 30_000 });
  await expect(firstResult).toContainText("Fake Harness Review Document");
  await capture(page, "knowledge-center--results--1440x900.png");

  // Compact: the same results render in the single main pane and Use as
  // context is directly reachable there.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByTestId("nav-home").click();
  await page.getByRole("button", { name: "Knowledge Center" }).click();
  for (let attempt = 0; attempt < 20; attempt++) {
    await input.fill(phrase);
    await page.getByTestId("knowledge-search-submit").click();
    if ((await firstResult.count()) > 0) break;
    await page.waitForTimeout(500);
  }
  await expect(firstResult).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("knowledge-use-as-context").first()).toBeVisible();
  await capture(page, "knowledge-center--results--390x844.png");
  await page.getByTestId("knowledge-use-as-context").first().click();

  // Expanded: hit pinned as Agent context chip.
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.getByTestId("context-chip")).toHaveCount(1);
  await page.getByRole("button", { name: "Close Knowledge Center" }).click();
  await expect(page.getByTestId("context-chip")).toBeVisible();
  await capture(page, "agent-center--context-chip--1440x900.png");

  // Expanded: granted opaque web-bundle app with knowledge results.
  await registerKnowledgeApp(page, stamp, appId);
  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("checkbox", { name: "knowledge.read" }).check();
  await page.getByRole("button", { name: "Install with 1 permission" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  await expect(page.frameLocator(".app-surface-frame").locator("#root")).toHaveText(
    "bridge-ready",
    { timeout: libraryTimeout },
  );
  const frameQuery = page.frameLocator(".app-surface-frame").locator("#query");
  for (let attempt = 0; attempt < 40; attempt++) {
    await frameQuery.fill(phrase);
    await page.frameLocator(".app-surface-frame").locator("#search").click();
    if ((await page.frameLocator(".app-surface-frame").locator("#results li").count()) > 0) break;
    await page.waitForTimeout(500);
  }
  await expect(page.frameLocator(".app-surface-frame").locator("#results li").first()).toBeVisible({
    timeout: 30_000,
  });
  await page.getByRole("button", { name: "Close App Library" }).click();
  await capture(page, "app-knowledge-search--results--1440x900.png");
  await page.getByRole("button", { name: "Close App", exact: true }).click();
});
