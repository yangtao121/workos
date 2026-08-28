import { expect, test } from "@playwright/test";

// The persistent acceptance volume accumulates registered apps across runs,
// so the catalog walk in the App Library spans many pages; give the library
// time to finish loading before asserting on specific rows.
const libraryTimeout = 30_000;

// The unique text the bundle's own script writes into the DOM. It proves the
// iframe executes the uploaded app.js instead of any Desktop-fixed HTML.
const marker = () => `surface-e2e-${String(Date.now())}`;

test.setTimeout(180_000);

test("opens an installed web bundle in a sandboxed surface and revokes it after removal", async ({
  page,
}) => {
  const stamp = String(Date.now());
  const appId = `e2e-surface-${stamp}`;
  const proof = marker();

  // 1. Upload a synthetic two-file bundle through the public Artifact API.
  //    The bundle's own script probes storage with a clearly synthetic key
  //    and renders the outcome: inside a sandboxed (opaque-origin) document
  //    any localStorage access throws, so the DOM shows `…|ls-opaque`.
  const html = `<!doctype html><title>Surface E2E</title><div id="root">static</div><script src="app.js"></script>`;
  const script = `var outcome = 'ls-opaque';
try {
  window.localStorage.setItem('workos-e2e-synthetic-probe', 'write');
  outcome = 'ls-readable-' + String(window.localStorage.getItem('workos-e2e-synthetic-probe'));
} catch (storageError) {
  outcome = 'ls-opaque';
}
document.getElementById('root').textContent = '${proof}|' + outcome;`;
  const artifactResponse = await page.request.post(
    "/workos.artifact.v1.ArtifactService/CreateArtifact",
    {
      data: {
        idempotencyKey: `e2e-artifact-${stamp}`,
        artifact: { title: `Surface E2E ${stamp}` },
        webBundle: {
          entrypoint: "index.html",
          files: [
            { path: "index.html", content: btoa(html) },
            { path: "app.js", content: btoa(script) },
          ],
        },
      },
    },
  );
  expect(artifactResponse.ok()).toBeTruthy();
  const artifact = (await artifactResponse.json()) as {
    artifact: { id: string; digest: string };
  };
  expect(artifact.artifact.digest).toMatch(/^sha256:[0-9a-f]{64}$/);

  // 2. Register an app version pinned to that exact bundle digest.
  const manifest = `apiVersion: workos.app/v1
id: ${appId}
name: Surface E2E ${stamp}
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
permissions: [artifact.read]
resources: {}
health: {}
maintainer: {}
`;
  const registerResponse = await page.request.post(
    "/workos.app.v1.AppRegistryService/RegisterApp",
    {
      data: {
        idempotencyKey: `e2e-surface-register-${stamp}`,
        manifestYaml: btoa(manifest),
      },
    },
  );
  expect(registerResponse.ok()).toBeTruthy();

  // 3. Create a project and install the app through the real UI.
  await page.goto("/");
  await page.getByLabel("Project name").fill(`E2E Surface ${stamp}`);
  await page.getByRole("button", { name: "Create space" }).click();
  await expect(page.locator(".project-card.active")).toContainText("E2E Surface");
  // A synthetic origin-storage marker the sandboxed surface must never be
  // able to read later (no real credential is ever used as a marker).
  await page.evaluate(() => {
    window.localStorage.setItem("workos-e2e-synthetic-probe", "origin-value");
  });

  await page.getByRole("button", { name: "App Library" }).click();
  const library = page.locator(".app-library");
  const row = library.locator(".app-row", { hasText: appId });
  await expect(row.getByText(`${appId} · registry 1.0.0`)).toBeVisible({
    timeout: libraryTimeout,
  });
  await row.getByRole("button", { name: "Install", exact: true }).click();
  await expect(row.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });

  // 4. Open it: a sandboxed iframe appears and the bundle's script runs,
  //    writing the unique proof plus the storage outcome into the DOM. The
  //    opaque-origin sandbox makes the storage probe throw.
  await row.getByRole("button", { name: "Open", exact: true }).click();
  const frame = page.locator(".app-surface-frame");
  await expect(frame).toBeVisible({ timeout: libraryTimeout });
  expect(await frame.getAttribute("sandbox")).toBe("allow-scripts");
  expect(await frame.getAttribute("sandbox")).not.toContain("allow-same-origin");
  expect(await frame.getAttribute("referrerpolicy")).toBe("no-referrer");
  const surfaceUrl = (await frame.getAttribute("src")) as string;
  expect(surfaceUrl).toMatch(/^\/surfaces\/[0-9a-f-]{36}\/$/);

  const frameLocator = page.frameLocator(".app-surface-frame");
  await expect(frameLocator.locator("#root")).toHaveText(`${proof}|ls-opaque`, {
    timeout: libraryTimeout,
  });
  // The frame's own JS context is equally storage-less.
  const surfaceFrame = page.frames().find((candidate) => candidate.url().includes("/surfaces/"));
  const frameProbe = await surfaceFrame?.evaluate(() => {
    try {
      window.localStorage.getItem("workos-e2e-synthetic-probe");
      return "storage-readable";
    } catch {
      return "storage-opaque";
    }
  });
  expect(frameProbe).toBe("storage-opaque");

  // 5. Reload the desktop and open the app again: the durable chain serves a
  //    fresh surface for the still-installed instance.
  await page.reload();
  await expect(page.locator(".project-card.active")).toContainText("E2E Surface");
  await page.getByRole("button", { name: "App Library" }).click();
  const reloadedRow = page.locator(".app-library .app-row", { hasText: appId });
  await expect(reloadedRow.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await reloadedRow.getByRole("button", { name: "Open", exact: true }).click();
  const reopenedFrame = page.locator(".app-surface-frame");
  await expect(reopenedFrame).toBeVisible({ timeout: libraryTimeout });
  const reopenedUrl = (await reopenedFrame.getAttribute("src")) as string;
  await expect(page.frameLocator(".app-surface-frame").locator("#root")).toHaveText(
    `${proof}|ls-opaque`,
    { timeout: libraryTimeout },
  );

  // 6. Top-level proof of the server-enforced sandbox: opening the same live
  //    surface URL directly (no iframe attribute involved) still yields a
  //    sandboxed opaque document — the response CSP carries
  //    `sandbox allow-scripts`, the script still runs, and the WorkOS origin
  //    storage marker stays unreachable.
  const topLevel = await page.request.get(reopenedUrl);
  expect(topLevel.headers()["content-security-policy"]).toContain("sandbox allow-scripts");
  expect(topLevel.headers()["content-security-policy"]).not.toContain("allow-same-origin");
  await topLevel.dispose();
  await page.goto(reopenedUrl);
  await expect(page.locator("#root")).toHaveText(`${proof}|ls-opaque`, {
    timeout: libraryTimeout,
  });
  expect(
    await page.evaluate(() => {
      try {
        const value = window.localStorage.getItem("workos-e2e-synthetic-probe");
        return `read:${String(value)}`;
      } catch {
        return "opaque";
      }
    }),
  ).toBe("opaque");

  // 7. Back to the desktop: window state does not survive top-level
  //    navigation, so open the app once more, then close the window — the
  //    session is revoked server-side and the old URL stops serving.
  await page.goto("/");
  await expect(page.locator(".project-card.active")).toContainText("E2E Surface");
  await page.getByRole("button", { name: "App Library" }).click();
  const activeRow = page.locator(".app-library .app-row", { hasText: appId });
  await expect(activeRow.getByText(/Installed · pinned 1\.0\.0/)).toBeVisible({
    timeout: libraryTimeout,
  });
  await activeRow.getByRole("button", { name: "Open", exact: true }).click();
  const finalFrame = page.locator(".app-surface-frame");
  await expect(finalFrame).toBeVisible({ timeout: libraryTimeout });
  const finalUrl = (await finalFrame.getAttribute("src")) as string;
  await page.getByRole("button", { name: "Close App", exact: true }).click();
  await expect(finalFrame).toHaveCount(0);
  const closedResponse = await page.request.get(finalUrl);
  expect(closedResponse.status()).toBe(404);

  // 8. Uninstall the app; the earlier session URL keeps failing closed.
  await activeRow.getByRole("button", { name: "Remove" }).click();
  await expect(activeRow.getByRole("button", { name: "Install", exact: true })).toBeVisible({
    timeout: libraryTimeout,
  });
  const uninstalledResponse = await page.request.get(surfaceUrl);
  expect(uninstalledResponse.status()).toBe(404);

  // The asset route never falls back to the Desktop SPA.
  const body = await uninstalledResponse.text();
  expect(body).not.toContain('<div id="root">');
});
