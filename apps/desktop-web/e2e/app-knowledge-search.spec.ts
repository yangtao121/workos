import { expect, test, type Page } from "@playwright/test";

// The app knowledge-search acceptance gate (ADR-0013): a deterministic web
// bundle whose manifest requests `knowledge.read` is installed with an
// explicit grant, opened as an opaque-origin surface, and searches THIS
// project's knowledge through the real chain
// iframe → app-host → Gateway → runtime-host → Core re-authorization →
// indexer projection. The same gate proves fail-closed behavior: without the
// grant the method is never negotiated, and after a revocation the stale
// session's calls are denied before the indexer is touched.
const libraryTimeout = 30_000;

test.setTimeout(240_000);

// The fixture implements the frame side of the versioned App Bridge protocol
// exactly like @workos/app-sdk: it acks the parent hello's nonce on the
// transferred port, advertises the methods it was offered, and exposes a
// bounded knowledge search UI. Results render as inert text only.
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
  methods.textContent = 'methods:' + (hello.methods || []).join(',');
  document.body.appendChild(methods);
  var input = document.createElement('input');
  input.id = 'query'; input.setAttribute('aria-label', 'App knowledge query');
  document.body.appendChild(input);
  var button = document.createElement('button');
  button.id = 'search'; button.textContent = 'Search project knowledge';
  document.body.appendChild(button);
  var out = document.createElement('ul');
  out.id = 'results'; out.setAttribute('aria-label', 'App knowledge results');
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
  try {
    var result = await request('knowledge.search', { query: query, pageSize: 20, pageToken: '' });
    var hitList = result.hits || [];
    if (hitList.length === 0) {
      var none = document.createElement('li'); none.id = 'no-hits'; none.textContent = 'no-hits';
      out.appendChild(none); return;
    }
    hitList.forEach(function (hit) {
      var li = document.createElement('li');
      li.setAttribute('data-artifact-id', hit.artifactId);
      li.setAttribute('data-digest', hit.digest);
      li.setAttribute('data-type', hit.artifactType);
      var title = document.createElement('strong'); title.textContent = hit.title;
      var excerpt = document.createElement('span'); excerpt.textContent = hit.excerpt;
      li.appendChild(title); li.appendChild(excerpt);
      out.appendChild(li);
    });
  } catch (error) {
    var failed = document.createElement('li');
    failed.id = 'search-error';
    failed.textContent = 'search-error:' + String(error.message).replace(/[^a-z_]/g, '');
    out.appendChild(failed);
  }
}
`;

async function createBundle(page: Page, stamp: string, marker: string) {
  const html = `<!doctype html><title>Knowledge E2E ${marker}</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-knowledge-artifact-${stamp}`,
        artifact: { title: `Knowledge E2E App ${marker}` },
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
name: Knowledge E2E App
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
        idempotencyKey: `e2e-knowledge-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("granted app searches project knowledge and fails closed on revoke", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `e2e-app-knowledge-${stamp}`;
  const phrase = "deterministic synthetic output";

  // Project + review artifact through the real fake-harness chain via the
  // public RPCs (the owner UI journey has its own dedicated gate). The
  // desktop's active project is pinned via sessionStorage BEFORE the first
  // navigation so the install lands in exactly this project.
  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-knowledge-project-${stamp}`,
        name: `Knowledge E2E ${stamp}`,
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();
  const created = (await createResponse.json()) as {
    project: { id: string; revision: string };
  };
  const projectId = created.project.id;
  await page.request.post(
    "/workos.project.v1.ProjectHarnessBindingService/SetProjectHarnessBinding",
    {
      data: {
        projectId,
        expectedRevision: created.project.revision,
        selection: { providerId: "fake" },
      },
    },
  );
  await page.request.post("/workos.agent.v1.AgentTaskService/SubmitTask", {
    data: {
      idempotencyKey: `e2e-knowledge-task-${stamp}`,
      input: {
        targetScope: { projectId },
        role: "general",
        goal: "produce the knowledge fixture review",
        outputArtifactTypes: ["document.markdown.v1"],
      },
    },
  });
  // Poll the authoritative Core list until the fake document materializes.
  let artifactId = "";
  for (let attempt = 0; attempt < 60 && artifactId === ""; attempt++) {
    const listed = await page.request.post("/workos.artifact.v1.ArtifactService/ListArtifacts", {
      data: { projectId, page: { pageSize: 10 } },
    });
    if (listed.ok()) {
      const body = (await listed.json()) as { artifacts?: { id: string }[] };
      // Proto3 JSON omits empty repeated fields: only an array with entries
      // can produce an id.
      const first = Array.isArray(body.artifacts) ? body.artifacts[0] : undefined;
      if (first) artifactId = first.id;
    }
    if (artifactId === "") await page.waitForTimeout(500);
  }
  expect(artifactId).not.toBe("");
  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");

  // Bundle + registration requesting knowledge.read.
  const artifact = await createBundle(page, stamp, "granted");
  await registerApp(page, stamp, appId, artifact.artifact, "knowledge.read");

  // Install with explicit consent: knowledge.read is the only granted
  // permission, so the negotiated methods are exactly knowledge.search.
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

  // Open the opaque surface; the hello must offer exactly knowledge.search.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  expect(await frame.getAttribute("sandbox")).toBe("allow-scripts");
  expect(await frame.getAttribute("sandbox")).not.toContain("allow-same-origin");
  const frameRoot = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameRoot()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  await expect(page.frameLocator(".app-surface-frame").locator("#methods")).toHaveText(
    "methods:knowledge.search",
  );

  // Search the project knowledge: the hit must carry the exact artifact
  // identity the owner-side projection holds, rendered as inert text.
  const frameQuery = page.frameLocator(".app-surface-frame").locator("#query");
  const firstHit = page.frameLocator(".app-surface-frame").locator("#results li").first();
  // Bounded polling: the durable ingestion may lag the artifact by a moment,
  // so re-issue the search until the hit surfaces (never a fixed sleep).
  const searchButton = page.frameLocator(".app-surface-frame").locator("#search");
  for (let attempt = 0; attempt < 40; attempt++) {
    await frameQuery.fill(phrase);
    await searchButton.click();
    if (
      (await firstHit.count()) > 0 &&
      (await firstHit.getAttribute("data-artifact-id")) !== null
    ) {
      break;
    }
    await page.waitForTimeout(500);
  }
  await expect(firstHit).toBeVisible({ timeout: 30_000 });
  await expect(firstHit).toContainText("Fake Harness Review Document");
  expect(await firstHit.getAttribute("data-artifact-id")).toBe(artifactId);
  expect(await firstHit.getAttribute("data-digest")).toMatch(/^sha256:[0-9a-f]{64}$/);

  // Revoke the grant while the surface stays open: the stale session's very
  // next call is denied at the Core grant-revision comparison, before the
  // indexer is ever touched (the revoke action itself uses the public RPC
  // directly; the desktop Manage-permissions flow has its own gate).
  const listResponse = await page.request.post(
    "/workos.app.v1.AppInstallationService/ListInstalledApps",
    { data: { projectId, pageSize: 50 } },
  );
  expect(listResponse.ok()).toBeTruthy();
  const installations = (await listResponse.json()) as {
    installations: { id: string; appId: string }[];
  };
  const mine = installations.installations.find((entry) => entry.appId === appId);
  expect(mine).toBeTruthy();
  const projectNow = await page.request.post("/workos.project.v1.ProjectService/GetProject", {
    data: { projectId },
  });
  const revision = ((await projectNow.json()) as { project: { revision: string } }).project
    .revision;
  const revokeResponse = await page.request.post(
    "/workos.app.v1.AppInstallationService/SetAppGrants",
    {
      data: {
        idempotencyKey: `e2e-knowledge-revoke-${stamp}`,
        projectId,
        installationId: mine === undefined ? "" : mine.id,
        expectedProjectRevision: revision,
        grantedPermissions: [],
      },
    },
  );
  expect(revokeResponse.ok()).toBeTruthy();

  await frameQuery.fill(phrase);
  await searchButton.click();
  await expect(page.frameLocator(".app-surface-frame").locator("#search-error")).toHaveText(
    "search-error:permission_denied",
    { timeout: 30_000 },
  );
});

test("an app without knowledge.read never negotiates knowledge.search", async ({ page }) => {
  const stamp = String(Date.now());
  const appId = `e2e-app-agentonly-${stamp}`;

  await page.goto("/");
  await page.getByLabel("Project name").fill(`Agent Only ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("Agent Only");

  const artifact = await createBundle(page, stamp, "agentonly");
  await registerApp(page, stamp, appId, artifact.artifact, "agent.task.run, agent.event.watch");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  for (const capability of ["agent.task.run", "agent.event.watch"]) {
    await dialog.getByRole("checkbox", { name: capability }).check();
  }
  await page.getByRole("button", { name: "Install with 2 permissions" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frameRoot = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameRoot()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  // The agent grant set must not implicitly carry knowledge.search.
  await expect(page.frameLocator(".app-surface-frame").locator("#methods")).toHaveText(
    "methods:agent.run,agent.stream",
  );
});
