// Captures deterministic desktop-web UI evidence per docs/ui/README.md.
// Runs inside the workos-playwright image against a real compose stack:
//   NODE_PATH=/usr/local/lib/node_modules MODE=before \
//   OUT_DIR=docs/ui/desktop-web/changes/<task>/before \
//   node tools/ui/capture-desktop.mjs
// Only synthetic fixture data is used; no credentials are involved.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");

const MODE = process.env.MODE ?? "before";
const OUT_DIR = process.env.OUT_DIR;
if (!OUT_DIR) {
  console.error("OUT_DIR is required");
  process.exit(2);
}
const BASE_URL = process.env.WORKOS_E2E_URL ?? "http://127.0.0.1:8080";
const VIEWPORT = { width: 1440, height: 900 };
const libraryTimeout = 30_000;

// Run-unique ids keep the persistent acceptance volume clean; the visible
// fixture names stay deterministic so screenshots stay comparable.
const stamp = String(Date.now());
const appId = `e2e-fixture-surface-${stamp}`;
const projectName = `Fixture Space ${stamp}`;

function b64(value) {
  return Buffer.from(value, "utf8").toString("base64");
}

// The synthetic bundle writes a deterministic marker; MODE=after extends the
// same bundle with a bridge client that runs a real project task and renders
// the Fake Harness terminal result.
function bundleFiles(mode) {
  const html = `<!doctype html><title>Fixture Surface</title><div id="root">static</div><script src="app.js"></script>`;
  let script;
  if (mode === "after") {
    script = [
      "const root = document.getElementById('root');",
      "root.textContent = 'bridge-pending';",
      "let port = null; let nonce = null; let nextId = 0; const pending = new Map();",
      "window.addEventListener('message', (event) => {",
      "  if (event.source !== window.parent) return;",
      "  const hello = event.data;",
      "  if (!hello || hello.version !== 'workos.app-bridge/v1' || hello.type !== 'hello') return;",
      "  if (event.ports.length !== 1) return;",
      "  port = event.ports[0]; nonce = hello.nonce;",
      "  port.start();",
      "  port.postMessage({ version: hello.version, type: 'ack', nonce: nonce });",
      "  root.textContent = 'bridge-ready';",
      "  port.onmessage = (message) => {",
      "    const envelope = message.data;",
      "    const pendingEntry = pending.get(envelope.requestId);",
      "    if (!pendingEntry) return;",
      "    pending.delete(envelope.requestId);",
      "    if (envelope.type === 'response') {",
      "      pendingEntry.resolve(envelope.payload);",
      "    } else if (envelope.type === 'event') {",
      "      if (pendingEntry.onEvent) pendingEntry.onEvent(envelope.payload);",
      "      pending.set(envelope.requestId, pendingEntry);",
      "    } else if (envelope.type === 'error') {",
      "      pendingEntry.reject(new Error(String(envelope.code)));",
      "    }",
      "  };",
      "  const runButton = document.createElement('button');",
      "  runButton.id = 'run-task';",
      "  runButton.textContent = 'Run project task';",
      "  runButton.addEventListener('click', async () => {",
      "    runButton.disabled = true;",
      "    try {",
      "      const result = await runTask();",
      "      root.textContent = 'terminal:' + result;",
      "    } catch (error) {",
      "      root.textContent = 'bridge-error:' + String(error.message);",
      "    }",
      "  });",
      "  document.body.appendChild(runButton);",
      "});",
      "function call(method, payload, onEvent) {",
      "  return new Promise((resolve, reject) => {",
      "    const requestId = 'req-' + String(++nextId);",
      "    pending.set(requestId, { resolve: resolve, reject: reject, onEvent: onEvent });",
      "    port.postMessage({ version: 'workos.app-bridge/v1', type: 'request', requestId: requestId, method: method, payload: payload });",
      "    setTimeout(() => { if (pending.delete(requestId)) reject(new Error('timeout')); }, 60000);",
      "  });",
      "}",
      "async function runTask() {",
      "  const run = await call('agent.run', { idempotencyKey: 'fixture-' + String(Date.now()) + '-' + String(Math.floor(Math.random() * 1e9)), goal: 'Summarize the fixture project state.', role: '' });",
      "  const summary = await new Promise((resolve, reject) => {",
      "    const entry = { resolve: resolve, reject: reject, onEvent: (event) => {",
      "      var oneof = event && event.event;",
      "      var completed = oneof && (oneof.case === 'runCompleted' ? oneof.value : oneof.runCompleted);",
      "      var failed = oneof && (oneof.case === 'runFailed' ? oneof.value : oneof.runFailed);",
      "      if (completed) resolve(String(completed.summary));",
      "      if (failed) reject(new Error('run-failed'));",
      "    } };",
      "    const requestId = 'req-' + String(++nextId);",
      "    pending.set(requestId, entry);",
      "    port.postMessage({ version: 'workos.app-bridge/v1', type: 'request', requestId: requestId, method: 'agent.stream', payload: { taskId: run.taskId, afterSequence: '0' } });",
      "    setTimeout(() => { if (pending.delete(requestId)) reject(new Error('stream-timeout')); }, 60000);",
      "  });",
      "  return summary;",
      "}",
    ].join("\n");
  } else {
    script = "document.getElementById('root').textContent = 'fixture-surface-ready';";
  }
  return [
    { path: "index.html", content: b64(html) },
    { path: "app.js", content: b64(script) },
  ];
}

async function createFixtureApp(request) {
  const artifactResponse = await request.post(
    `${BASE_URL}/workos.artifact.v1.ArtifactService/CreateArtifact`,
    {
      data: {
        idempotencyKey: `e2e-fixture-artifact-${stamp}`,
        artifact: { title: "Fixture Surface App" },
        webBundle: { entrypoint: "index.html", files: bundleFiles(MODE) },
      },
    },
  );
  if (!artifactResponse.ok())
    throw new Error(`artifact upload failed: ${artifactResponse.status()}`);
  const artifact = await artifactResponse.json();

  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Fixture Surface App
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
  const registerResponse = await request.post(
    `${BASE_URL}/workos.app.v1.AppRegistryService/RegisterApp`,
    {
      data: {
        idempotencyKey: `e2e-fixture-register-${stamp}`,
        manifestYaml: b64(manifest),
      },
    },
  );
  if (!registerResponse.ok()) throw new Error(`register failed: ${registerResponse.status()}`);
}

async function shoot(page, name) {
  const target = path.join(OUT_DIR, `${name}--1440x900.png`);
  await page.screenshot({ path: target, fullPage: false });
  console.log(`captured ${target}`);
}

// frameLocator text polling without the @playwright/test expect assertion API.
async function waitFrameText(page, selector, pattern, timeout = 90_000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    const text = await page
      .frameLocator(".app-surface-frame")
      .locator(selector)
      .textContent()
      .catch(() => null);
    if (text !== null && pattern.test(text.trim())) return text.trim();
    if (Date.now() > deadline)
      throw new Error(`frame text never matched ${pattern}: ${String(text)}`);
    await page.waitForTimeout(500);
  }
}

async function run() {
  fs.mkdirSync(OUT_DIR, { recursive: true });
  const browser = await chromium.launch();
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: 1,
  });
  const page = await context.newPage();
  page.on("console", (message) => console.log(`[console:${message.type()}]`, message.text()));
  page.on("requestfailed", (request) =>
    console.log("[requestfailed]", request.url(), request.failure()?.errorText),
  );
  try {
    await createFixtureApp(page.request);

    await page.goto(`${BASE_URL}/`);
    await page.getByLabel("Project name").fill(projectName);
    await page.getByRole("button", { name: "Create space" }).click();
    await page
      .locator(".project-card.active", { hasText: "Fixture Space" })
      .waitFor({ timeout: libraryTimeout });

    await page.getByRole("button", { name: "App Library" }).click();
    const library = page.locator(".app-library");
    const row = library.locator(".app-row", { hasText: appId });
    await row.waitFor({ timeout: libraryTimeout });

    if (MODE === "after") {
      await row.getByRole("button", { name: "Install", exact: true }).click();
      // Consent dialog: requested permissions listed, every checkbox unchecked.
      await page.getByRole("dialog").waitFor({ timeout: libraryTimeout });
      await shoot(page, "app-library--consent");
      await page.getByRole("checkbox", { name: "agent.task.run" }).check();
      await page.getByRole("checkbox", { name: "agent.event.watch" }).check();
      await shoot(page, "app-library--consent-selected");
      await page.getByRole("button", { name: "Install with 2 permissions" }).click();
      await row.getByText(/Installed · pinned 1\.0\.0/).waitFor({ timeout: libraryTimeout });
      await shoot(page, "app-library--installed-granted");
    } else {
      await row.getByRole("button", { name: "Install", exact: true }).click();
      await row.getByText(/Installed · pinned 1\.0\.0/).waitFor({ timeout: libraryTimeout });
      await shoot(page, "app-library--installed");
    }

    await row.getByRole("button", { name: "Open", exact: true }).click();
    const frame = page.locator(".app-surface-frame");
    await frame.waitFor({ timeout: libraryTimeout });
    if (MODE === "after") {
      await waitFrameText(page, "#root", /^bridge-ready$/);
      await page.frameLocator(".app-surface-frame").locator("#run-task").click();
      await waitFrameText(page, "#root", /^terminal:/);
      await shoot(page, "app-surface--bridge-result");
    } else {
      await waitFrameText(page, "#root", /^fixture-surface-ready$/);
      await shoot(page, "app-surface--ready");
    }
    console.log("capture complete");
  } finally {
    await browser.close();
  }
}

run().catch((error) => {
  console.error(error);
  process.exit(1);
});
