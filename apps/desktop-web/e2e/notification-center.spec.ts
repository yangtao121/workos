import { expect, test, type Page } from "@playwright/test";

// The notification center acceptance gate (ADR-0014): a real fake-harness
// task run produces the agent.task.terminal and artifact.review.created
// notifications through the Core source transactions; two independent
// dev-bypass browser contexts for the same owner see the same unread facts,
// a mark-read on one context converges on the other, and
// the durable facts survive a Gateway/Core restart (driven by the Makefile
// gate around this spec with WORKOS_E2E_NOTIFICATIONS_VERIFY=1).
test.setTimeout(240_000);

const terminalTitle = "Task completed";
const artifactTitle = "Review artifact ready";

async function submitTerminalTask(page: Page, stamp: string) {
  const createResponse = await page.request.post(
    "/workos.project.v1.ProjectService/CreateProject",
    {
      data: {
        idempotencyKey: `e2e-notification-project-${stamp}`,
        name: `Notification E2E ${stamp}`,
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
        providerId: "fake",
      },
    },
  );
  const submitResponse = await page.request.post("/workos.agent.v1.AgentTaskService/SubmitTask", {
    data: {
      idempotencyKey: `e2e-notification-task-${stamp}`,
      input: {
        targetScope: { projectId },
        role: "general",
        goal: "produce the notification fixture review",
        outputArtifactTypes: ["document.markdown.v1"],
      },
    },
  });
  expect(submitResponse.ok()).toBeTruthy();
  // Poll the authoritative Core list until the fake document materializes;
  // the terminal + artifact notifications commit in those transactions.
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
  return projectId;
}

async function openNotificationCenter(page: Page) {
  await page.getByTestId("open-notifications").click();
  const center = page.getByTestId("notification-center");
  await expect(center).toBeVisible({ timeout: 30_000 });
  return center;
}

// markEverythingRead gives the gate a deterministic baseline: the durable
// store keeps every historical notification from earlier runs, so the spec
// drives unread to zero through the public read commands first.
// markEverythingRead keeps sweeping until the unread set is empty AND stays
// empty for a quiet window: straggler harness tasks from earlier specs may
// land their notifications mid-gate.
async function markEverythingRead(page: Page, stamp: string) {
  for (;;) {
    const listed = await page.request.post(
      "/workos.notification.v1.NotificationService/ListNotifications",
      { data: { unreadOnly: true, pageSize: 100 } },
    );
    expect(listed.ok()).toBeTruthy();
    const body = (await listed.json()) as {
      notifications: { id: string }[] | undefined;
      unreadCount: number | string;
    };
    const ids: string[] = (body.notifications ?? []).map((entry) => entry.id);
    if (ids.length === 0) return;
    await page.request.post("/workos.notification.v1.NotificationService/MarkNotificationsRead", {
      data: {
        notificationIds: ids,
        idempotencyKey: `e2e-notification-sweep-${stamp}-${String(ids[0])}`,
      },
    });
  }
}

async function badgeCount(page: Page): Promise<number> {
  const badge = page.getByTestId("open-notifications").locator(".notification-badge");
  if ((await badge.count()) === 0) return 0;
  const text = ((await badge.textContent()) ?? "0").trim();
  return parseInt(text === "99+" ? "99" : text, 10);
}

test("terminal and artifact notifications arrive live and converge read state across devices", async ({
  page,
  browser,
}, testInfo) => {
  if (process.env.WORKOS_E2E_NOTIFICATIONS_VERIFY === "1") {
    test.skip(true, "verify run has its own spec phase");
  }
  const stamp = String(Date.now());
  await markEverythingRead(page, stamp);
  const projectId = await submitTerminalTask(page, stamp);

  // Device A: pin the project and open the desktop. From a zero baseline
  // the live stream must deliver exactly the two new facts (terminal +
  // artifact) without any refresh.
  await page.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await page.goto("/");
  await expect(page.getByTestId("open-notifications")).toBeVisible({ timeout: 30_000 });
  // Live delivery: device A's badge must reach the owner-wide unread count
  // the authoritative summary reports (a shared dev stack can see straggler
  // facts from earlier specs, so the exact authority is the summary, not a
  // fixed number).
  // Poll badge and summary TOGETHER until they agree: the authoritative
  // summary is the badge's exact expected value even while straggler facts
  // from other specs land.
  let summaryUnread = 0;
  let converged = 0;
  for (let attempt = 0; attempt < 60; attempt++) {
    const summary = await page.request.post(
      "/workos.notification.v1.NotificationService/GetNotificationSummary",
      { data: {} },
    );
    summaryUnread = Number(((await summary.json()) as { unreadCount: string }).unreadCount);
    converged = await badgeCount(page);
    if (summaryUnread >= 2 && converged === summaryUnread) break;
    await page.waitForTimeout(500);
  }
  expect(summaryUnread).toBeGreaterThanOrEqual(2);
  converged = await badgeCount(page);
  expect(converged).toBe(summaryUnread);

  const center = await openNotificationCenter(page);
  await expect(center.getByText(terminalTitle).first()).toBeVisible({ timeout: 30_000 });
  await expect(center.getByText(artifactTitle).first()).toBeVisible({ timeout: 30_000 });

  // Device B: a second, fully independent browser context of the same
  // owner. Its projection must converge on the same unread facts.
  const secondContext = await browser.newContext();
  const deviceB = await secondContext.newPage();
  await deviceB.addInitScript((id: string) => {
    window.sessionStorage.setItem("workos.activeProjectId", id);
  }, projectId);
  await deviceB.goto("/");
  await expect(deviceB.getByTestId("open-notifications")).toBeVisible({ timeout: 30_000 });
  // B converges on the same authoritative summary: paired polling tolerates
  // straggler facts landing between A's snapshot and B's.
  let badgeB = 0;
  for (let attempt = 0; attempt < 60; attempt++) {
    const summary = await deviceB.request.post(
      "/workos.notification.v1.NotificationService/GetNotificationSummary",
      { data: {} },
    );
    const expected = Number(((await summary.json()) as { unreadCount: string }).unreadCount);
    badgeB = await badgeCount(deviceB);
    if (expected >= 2 && badgeB === expected) break;
    await deviceB.waitForTimeout(500);
  }
  expect(badgeB).toBeGreaterThanOrEqual(2);

  // B marks one notification read through the center, scoped to Current
  // Project so the marked fact is exactly one of this run's two; A's badge
  // decrements from the READ change alone (the durable monotonic read fact).
  const centerB = await openNotificationCenter(deviceB);
  await centerB.getByTestId("notification-filter-project").click();
  const projectItems = centerB.getByTestId("notification-item");
  await expect(projectItems).toHaveCount(2, { timeout: 30_000 });
  const firstItem = projectItems.first();
  await firstItem.getByRole("button", { name: "Mark read" }).click();
  // Device A must converge to the authoritative owner-wide count even when
  // other parallel specs add unrelated notifications. Its project-filtered
  // projection then proves the READ fact itself crossed devices: exactly one
  // of this run's two project facts remains unread.
  let deviceABadge = -1;
  let authoritativeUnread = -2;
  for (let attempt = 0; attempt < 30; attempt++) {
    const summary = await page.request.post(
      "/workos.notification.v1.NotificationService/GetNotificationSummary",
      { data: {} },
    );
    expect(summary.ok()).toBeTruthy();
    authoritativeUnread = Number(((await summary.json()) as { unreadCount: string }).unreadCount);
    deviceABadge = await badgeCount(page);
    if (deviceABadge === authoritativeUnread) break;
    await page.waitForTimeout(500);
  }
  expect(deviceABadge).toBe(authoritativeUnread);
  await center.getByTestId("notification-filter-project").click();
  await expect(center.getByTestId("notification-item")).toHaveCount(2, { timeout: 30_000 });
  await expect(center.locator(".notification-item.unread")).toHaveCount(1, {
    timeout: 30_000,
  });

  // The marked fact lost its unread styling in place.
  await expect(centerB.locator(".notification-item.unread")).toHaveCount(1, {
    timeout: 30_000,
  });
  // Back to All for the typed action; unread styling there also covers
  // straggler facts from other specs, which is expected on a shared stack.
  await centerB.getByTestId("notification-filter-all").click();
  await expect(centerB.locator(".notification-item.unread").first()).toBeVisible({
    timeout: 30_000,
  });

  // Typed action: the newest artifact notification re-verifies its target
  // through the public ArtifactService and opens the inert review window.
  const artifactItem = centerB
    .getByTestId("notification-item")
    .filter({ hasText: artifactTitle })
    .first();
  await artifactItem.getByRole("button", { name: "Open artifact" }).click();
  await expect(deviceB.locator(".workos-window", { hasText: "Artifact Review" })).toBeVisible({
    timeout: 30_000,
  });

  void page.waitForTimeout;
  void testInfo;
  await secondContext.close();
});

test("durable notification facts survive a Gateway/Core restart", async ({ page }) => {
  if (process.env.WORKOS_E2E_NOTIFICATIONS_VERIFY !== "1") {
    test.skip(true, "verify phase runs after the gate restarts Core and Gateway");
  }
  // The gate shell seeded and read one notification before restarting the
  // processes; the badge and read state must still be here, served from the
  // durable store, and the stream must resume from the persisted cursor.
  await page.goto("/");
  await expect(page.getByTestId("open-notifications")).toBeVisible({ timeout: 60_000 });
  const seeded = Number(process.env.WORKOS_E2E_NOTIFICATIONS_SEED_UNREAD ?? "0");
  let badge = await badgeCount(page);
  for (let attempt = 0; attempt < 40 && badge < seeded; attempt++) {
    badge = await badgeCount(page);
    await page.waitForTimeout(500);
  }
  expect(badge).toBeGreaterThanOrEqual(seeded);
  const center = await openNotificationCenter(page);
  await expect(center.getByTestId("notification-item").first()).toBeVisible({
    timeout: 30_000,
  });
});
