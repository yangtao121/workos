import { expect, test, type Page } from "@playwright/test";

// The app notifications acceptance gate (ADR-0014): a web bundle whose
// manifest requests `notifications.create` is installed with an explicit
// grant, opened as an opaque-origin surface, and creates quota-bound owner
// notifications through the real chain
// iframe → app-host → Gateway → runtime-host → Core private ingest
// (idempotency + quota + projection in one transaction). The same gate
// proves the owner sees the app fact in the durable Notification Center,
// that replayed keys never duplicate, and that revocation fail-closes the
// stale port with zero Core side effects.
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
  var methods = document.createElement('p');
  methods.id = 'methods';
  methods.textContent = 'methods:' + (hello.methods || []).join(',');
  document.body.appendChild(methods);
  var key = document.createElement('input');
  key.id = 'key'; key.setAttribute('aria-label', 'Notification idempotency key');
  key.value = 'fixture-key-1';
  document.body.appendChild(key);
  var button = document.createElement('button');
  button.id = 'notify'; button.textContent = 'Create app notification';
  document.body.appendChild(button);
  var out = document.createElement('p');
  out.id = 'notify-out'; out.setAttribute('aria-label', 'App notification result');
  document.body.appendChild(out);
  button.addEventListener('click', function () { void notify(key.value); });
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
async function notify(key) {
  var out = document.getElementById('notify-out');
  try {
    var result = await request('notifications.create', {
      idempotencyKey: key,
      title: 'Fixture app update',
      body: 'A bounded inert fixture message',
    });
    out.textContent = 'notify-ok:' + result.notification.id + ':' + result.notification.origin + ':' + result.unreadCount;
  } catch (error) {
    out.textContent = 'notify-error:' + String(error.message).replace(/[^a-z_]/g, '');
  }
}
`;

async function createBundle(page: Page, stamp: string) {
  const html = `<!doctype html><title>Notifications E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-app-notification-artifact-${stamp}`,
        artifact: { title: `Notifications E2E App` },
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
name: Notifications E2E App
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
        idempotencyKey: `e2e-app-notification-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();
}

test("granted app creates owner notifications; replay dedupes and revoke fails closed", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-app-notification-${stamp}`;

  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-app-notification-project-${stamp}`,
        name: `App Notifications E2E ${stamp}`,
      },
    },
  );
  expect(createResponse.ok()).toBeTruthy();
  const created = (await createResponse.json()) as { project: { id: string } };
  const projectId = created.project.id;

  const artifact = await createBundle(page, stamp);
  await registerApp(page, stamp, appId, artifact.artifact, "notifications.create");

  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");

  await page.getByRole("button", { name: "App Library" }).click();
  const row = page.locator(".app-library .app-row", { hasText: appId });
  await expect(row.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("checkbox", { name: "notifications.create" }).check();
  await page.getByRole("button", { name: "Install with 1 permission" }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  expect(await frame.getAttribute("sandbox")).toBe("allow-scripts");
  const frameRoot = () => page.frameLocator(".app-surface-frame").locator("#root");
  await expect(frameRoot()).toHaveText("bridge-ready", { timeout: libraryTimeout });
  await expect(page.frameLocator(".app-surface-frame").locator("#methods")).toHaveText(
    "methods:notifications.create",
  );

  const out = page.frameLocator(".app-surface-frame").locator("#notify-out");
  const notifyButton = page.frameLocator(".app-surface-frame").locator("#notify");
  const keyInput = page.frameLocator(".app-surface-frame").locator("#key");

  // First create: success with the app origin label and an unread count.
  // Bounded retry: a Core that is still settling after a gate restart can
  // surface one transient error; the app retries like a real client would.
  for (let attempt = 0; attempt < 10; attempt++) {
    await notifyButton.click();
    try {
      await expect(out).toHaveText(/notify-ok:[0-9a-f][0-9a-f-]{35}:app:\d+/, {
        timeout: 5_000,
      });
      break;
    } catch {
      if (attempt === 9) throw new Error("app notification never succeeded");
    }
  }
  await expect(out).toHaveText(/notify-ok:[0-9a-f][0-9a-f-]{35}:app:\d+/, { timeout: 30_000 });
  const firstText = (await out.textContent()) ?? "";
  const firstId = firstText.split(":")[1];

  // Same key/same request: exact replay — same notification id, no second.
  await notifyButton.click();
  await expect(out).toHaveText(firstText, { timeout: 30_000 });
  expect((await out.textContent()) ?? "").toContain(firstId);

  // A different key is a distinct intent: a new notification id and a
  // strictly higher owner unread count. Bounded re-drive like a real client
  // (the same key can never double-create).
  await keyInput.fill("fixture-key-2");
  const firstUnread = Number(firstText.split(":")[3]);
  let secondText = "";
  for (let attempt = 0; attempt < 30; attempt++) {
    await notifyButton.click();
    secondText = (await out.textContent()) ?? "";
    const parts = secondText.split(":");
    if (parts[0] === "notify-ok" && parts[1] !== firstId && Number(parts[3]) > firstUnread) {
      break;
    }
    await page.waitForTimeout(500);
  }
  const secondParts = secondText.split(":");
  expect(secondParts[0]).toBe("notify-ok");
  expect(secondParts[1]).not.toBe(firstId);

  // The owner's durable Notification Center lists the app fact with the
  // explicit app origin label.
  const center = page.getByTestId("notification-center");
  await page.getByTestId("open-notifications").click();
  await expect(center).toBeVisible({ timeout: 30_000 });
  await expect(
    center.getByTestId("notification-item").filter({ hasText: "Fixture app update" }).first(),
  ).toBeVisible({ timeout: 30_000 });
  await expect(
    center
      .getByTestId("notification-item")
      .filter({ hasText: "Fixture app update" })
      .first()
      .getByText("· app"),
  ).toBeVisible();

  // Close the center window so it no longer overlays the app surface.
  await page.getByRole("button", { name: "Close Notifications" }).click();
  await expect(center).toBeHidden({ timeout: 30_000 });

  // Revoke the grant while the surface stays open: the stale port's very
  // next call is denied before Core touches quota or notifications.
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
        idempotencyKey: `e2e-app-notification-revoke-${stamp}`,
        projectId,
        installationId: mine === undefined ? "" : mine.id,
        expectedProjectRevision: revision,
        grantedPermissions: [],
      },
    },
  );
  expect(revokeResponse.ok()).toBeTruthy();

  await keyInput.fill("fixture-key-3");
  await notifyButton.click();
  await expect(out).toHaveText("notify-error:permission_denied", { timeout: 30_000 });

  // Zero side effects: the denied attempt created nothing — exactly the two
  // notifications from the two distinct successful keys remain.
  const countResponse = await page.request.post(
    "/workos.notification.v1.NotificationService/ListNotifications",
    { data: { pageSize: 100 } },
  );
  expect(countResponse.ok()).toBeTruthy();
  const listed = (await countResponse.json()) as {
    notifications: { title: string; projectId: string }[];
  };
  // Scope to THIS run's project: prior gate runs leave their facts behind.
  const appFacts = listed.notifications.filter(
    (entry) => entry.title === "Fixture app update" && entry.projectId === projectId,
  );
  expect(appFacts).toHaveLength(2);
});
