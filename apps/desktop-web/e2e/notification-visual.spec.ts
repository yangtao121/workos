import { expect, test, type Page } from "@playwright/test";

// Deterministic visual capture for the local-first notification slice
// (ADR-0014). Real chain: a fake-harness task produces terminal + artifact
// notifications, and a granted app creates one app-origin fact. Fixed
// fixture data only — no real user content, no random identifiers in frame,
// no credentials. Screenshots land in WORKOS_CAPTURE_DIR as the task's
// after/ evidence; the Makefile gate copies them into current/.
const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";
const libraryTimeout = 30_000;

test.setTimeout(300_000);

// A minimal app fixture that creates one app notification with a fixed key
// when clicked, rendering the projected fact as inert text.
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
  var button = document.createElement('button');
  button.id = 'notify'; button.textContent = 'Notify';
  document.body.appendChild(button);
  var out = document.createElement('p');
  out.id = 'notify-out';
  document.body.appendChild(out);
  button.addEventListener('click', function () { void notify(); });
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
async function notify() {
  try {
    var result = await request('notifications.create', {
      idempotencyKey: 'visual-fixture-key-1',
      title: 'Fixture app update',
      body: 'A bounded inert fixture message',
    });
    document.getElementById('notify-out').textContent = 'notify-ok';
  } catch (error) {
    document.getElementById('notify-out').textContent = 'notify-error';
  }
}
`;

async function capture(page: Page, name: string) {
  if (!captureDir) return;
  await page.screenshot({ path: `${captureDir}/${name}`, fullPage: false });
}

async function markEverythingRead(page: Page, stamp: string) {
  for (;;) {
    const listed = await page.request.post(
      "/workos.notification.v1.NotificationService/ListNotifications",
      { data: { unreadOnly: true, pageSize: 100 } },
    );
    const body = (await listed.json()) as { notifications?: { id: string }[] };
    const ids = (body.notifications ?? []).map((entry) => entry.id);
    if (ids.length === 0) return;
    await page.request.post("/workos.notification.v1.NotificationService/MarkNotificationsRead", {
      data: {
        notificationIds: ids,
        idempotencyKey: `visual-notification-sweep-${stamp}-${String(ids[0])}`,
      },
    });
  }
}

test("captures notification center evidence at three viewports", async ({ page }) => {
  test.skip(!captureDir, "visual capture runs explicitly");
  const stamp = String(Date.now());
  await markEverythingRead(page, stamp);

  // Project + fake-harness task with a review artifact: two system facts.
  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `visual-notification-project-${stamp}`,
        name: "Notification Visuals",
      },
    },
  );
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
        providerId: "fake",
      },
    },
  );
  await page.request.post("/workos.agent.v1.AgentTaskService/SubmitTask", {
    data: {
      idempotencyKey: `visual-notification-task-${stamp}`,
      input: {
        targetScope: { projectId },
        role: "general",
        goal: "visual fixture review",
        outputArtifactTypes: ["document.markdown.v1"],
      },
    },
  });
  let artifactId = "";
  for (let attempt = 0; attempt < 60 && artifactId === ""; attempt++) {
    const listed = await page.request.post("/workos.artifact.v1.ArtifactService/ListArtifacts", {
      data: { projectId, page: { pageSize: 10 } },
    });
    if (listed.ok()) {
      const body = (await listed.json()) as { artifacts?: { id: string }[] };
      const first = Array.isArray(body.artifacts) ? body.artifacts[0] : undefined;
      if (first) artifactId = first.id;
    }
    if (artifactId === "") await page.waitForTimeout(500);
  }
  expect(artifactId).not.toBe("");

  // Granted app + install + open; create the app-origin fact.
  const html = `<!doctype html><title>Notifications Visuals</title><div id="root">static</div><script src="app.js"></script>`;
  const bundleResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `visual-notification-bundle-${stamp}`,
        artifact: { title: "Notifications Visual App" },
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
  const bundle = (await bundleResponse.json()) as {
    artifact: { id: string; digest: string };
  };
  const appId = `visual-app-notification-${stamp}`;
  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Notifications Visual App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: ${bundle.artifact.id}
  artifactDigest: ${bundle.artifact.digest}
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [notifications.create]
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `visual-notification-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();

  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox", { name: "notifications.create" }).check();
  await page.getByRole("button", { name: "Install with 1 permission" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  await expect(frame.contentFrame().locator("#root")).toHaveText("bridge-ready", {
    timeout: libraryTimeout,
  });
  await frame.contentFrame().locator("#notify").click();
  await expect(frame.contentFrame().locator("#notify-out")).toHaveText("notify-ok", {
    timeout: 30_000,
  });

  // Expanded: bell with badge, then the unread center.
  let badge = 0;
  for (let attempt = 0; attempt < 40; attempt++) {
    const badgeLocator = page.getByTestId("open-notifications").locator(".notification-badge");
    if ((await badgeLocator.count()) > 0) {
      const text = ((await badgeLocator.textContent()) ?? "").trim();
      if (text !== "") {
        badge = text === "99+" ? 99 : parseInt(text, 10);
        if (badge >= 3) break;
      }
    }
    await page.waitForTimeout(500);
  }
  await capture(page, "notification-center--bell-badge--1440x900.png");
  await page.getByTestId("open-notifications").click();
  const center = page.getByTestId("notification-center");
  await expect(center).toBeVisible({ timeout: 30_000 });
  await expect(
    center.getByTestId("notification-item").filter({ hasText: "Fixture app update" }).first(),
  ).toBeVisible({ timeout: 30_000 });
  await expect(center.getByText("Task completed").first()).toBeVisible({ timeout: 30_000 });
  await capture(page, "notification-center--unread-list--1440x900.png");

  // Typed action over the artifact fact: the inert review window opens.
  const artifactItem = center
    .getByTestId("notification-item")
    .filter({ hasText: "Review artifact ready" })
    .first();
  await artifactItem.getByRole("button", { name: "Open artifact" }).click();
  await expect(page.locator(".workos-window", { hasText: "Artifact Review" })).toBeVisible({
    timeout: 30_000,
  });
  await capture(page, "notification-center--typed-action--1440x900.png");
  await page.getByRole("button", { name: "Close Artifact Review" }).click();
  await page.getByRole("button", { name: "Close Notifications" }).click();

  // Medium: dock reachability with unread badge.
  await page.setViewportSize({ width: 820, height: 1180 });
  await expect(page.getByTestId("toggle-dock")).toBeVisible({ timeout: 30_000 });
  await capture(page, "notification-center--bell-badge--820x1180.png");
  await page.getByTestId("toggle-dock").click();
  await page.getByTestId("adaptive-dock").getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByTestId("notification-center")).toBeVisible({ timeout: 30_000 });
  await capture(page, "notification-center--unread-list--820x1180.png");
  await page.getByRole("button", { name: "Close Notifications" }).click();

  // Compact: bottom nav reachability.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByTestId("nav-notifications")).toBeVisible({ timeout: 30_000 });
  await capture(page, "notification-center--bell-badge--390x844.png");
  await page.getByTestId("nav-notifications").click();
  await expect(page.getByTestId("notification-center")).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText("Fixture app update").first()).toBeVisible({ timeout: 30_000 });
  await capture(page, "notification-center--app-origin--390x844.png");
});
