import { expect, test, type Page } from "@playwright/test";

// Deterministic visual capture for the adaptive shell slice
// (docs/tasks/20260831-v1-runtime-reliability-adaptive-closeout.md). Run
// explicitly:
//   pnpm exec playwright test adaptive-visual.spec.ts
// with WORKOS_CAPTURE_DIR pointing at the task's docs/ui capture folder
// (before/ for the pre-change build, after/ for the post-change build) and
// WORKOS_E2E_URL pointing at a server serving the matching app bundle.
//
// Every network response is fixed before navigation: fixed device, fixed
// projects, fixed task/timeline, fixed catalog. No live backend, no real
// user data, no random UUIDs or wall-clock timestamps on screen. The
// pre-change build only ever rendered the free-window desktop, so states
// that build could not reach (single-pane agent view, apps overlay, agent
// slide-over, fold panes) have no before/ frame — notes.md documents that.

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "/captures";

test.use({
  deviceScaleFactor: 1,
  locale: "en-US",
  timezoneId: "UTC",
});

const TASK_ID = "01990000-0000-7000-8000-00000000a001";

const timelineEvents = [
  {
    event: {
      id: "01990000-0000-7000-8000-00000000a101",
      taskId: TASK_ID,
      sequence: "1",
      runStarted: { runId: "01990000-0000-7000-8000-00000000a201", providerId: "fake" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-00000000a102",
      taskId: TASK_ID,
      sequence: "2",
      assistantMessage: { text: "Fake harness received: Adaptive fixture goal" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-00000000a103",
      taskId: TASK_ID,
      sequence: "3",
      usageRecorded: { inputTokens: "18", outputTokens: "4", model: "fake/deterministic" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-00000000a104",
      taskId: TASK_ID,
      sequence: "4",
      runCompleted: { summary: "Task finished by the fake harness" },
    },
  },
];

function jsonHeaders(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

// connectFrame encodes one Connect streaming envelope: 1 zero byte (no
// compression) + 4 big-endian length bytes + the JSON payload, terminated by
// the EndStream frame the Connect client requires.
function connectFrames(messages: unknown[]): Buffer {
  const frames = messages.map((message) => {
    const payload = Buffer.from(JSON.stringify(message), "utf8");
    const header = Buffer.alloc(5);
    header.writeUInt32BE(payload.length, 1);
    return Buffer.concat([header, payload]);
  });
  const end = Buffer.from(JSON.stringify({ metadata: {} }), "utf8");
  const endHeader = Buffer.alloc(5);
  endHeader.writeUInt8(2, 0);
  endHeader.writeUInt32BE(end.length, 1);
  frames.push(Buffer.concat([endHeader, end]));
  return Buffer.concat(frames);
}

async function installFixtures(page: Page) {
  await page.route("**/workos.auth.v1.DeviceService/GetCurrentDevice", (route) =>
    route.fulfill(
      jsonHeaders({
        device: {
          deviceId: "01990000-0000-7000-8000-0000000000de",
          name: "Fixture Desktop",
          revision: "7",
          isCurrent: true,
        },
      }),
    ),
  );
  await page.route("**/workos.project.v1.ProjectService/ListProjects", (route) =>
    route.fulfill(
      jsonHeaders({
        projects: [
          {
            id: "01990000-0000-7000-8000-000000000001",
            ownerUserId: "01990000-0000-7000-8000-0000000000ff",
            name: "Fixture Project",
            icon: "◈",
            workspaceRefs: [],
            installedAppIds: [],
            defaultAgentRole: "general",
            revision: "3",
          },
        ],
      }),
    ),
  );
  await page.route("**/workos.harness.v1.HarnessCatalogService/GetHarnessCatalog", (route) =>
    route.fulfill(
      jsonHeaders({
        providers: [
          {
            id: "fake",
            displayName: "Deterministic Fake Harness",
            adapterVersion: "1.0.0",
            health: "HEALTH_STATE_HEALTHY",
            capabilities: {
              streaming: true,
              usageReporting: true,
              hardTokenBudget: true,
              hardRuntimeDeadline: true,
              structuredArtifacts: false,
            },
          },
        ],
        defaultProviderId: "fake",
      }),
    ),
  );
  await page.route("**/workos.agent.v1.AgentTaskService/SubmitTask", (route) =>
    route.fulfill(
      jsonHeaders({
        task: {
          id: TASK_ID,
          ownerUserId: "01990000-0000-7000-8000-0000000000ff",
          state: "AGENT_TASK_STATE_RUNNING",
          providerId: "fake",
          lastEventSequence: "4",
        },
      }),
    ),
  );
  await page.route("**/workos.agent.v1.AgentTaskService/WatchTaskEvents", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/connect+json",
      body: connectFrames(timelineEvents),
    }),
  );
  await page.route("**/workos.agent.v1.AgentTaskService/GetTask", (route) =>
    route.fulfill(
      jsonHeaders({
        task: {
          id: TASK_ID,
          ownerUserId: "01990000-0000-7000-8000-0000000000ff",
          state: "AGENT_TASK_STATE_COMPLETED",
          providerId: "fake",
          lastEventSequence: "4",
        },
      }),
    ),
  );
  await page.route("**/workos.incident.v1.IncidentService/ListIncidents", (route) =>
    route.fulfill(jsonHeaders({ incidents: [], page: { nextPageToken: "" } })),
  );
  await page.route("**/workos.app.v1.AppRegistryService/ListApps", (route) =>
    route.fulfill(jsonHeaders({ apps: [], page: { nextPageToken: "" } })),
  );
  await page.route("**/workos.app.v1.AppInstallationService/ListInstalledApps", (route) =>
    route.fulfill(jsonHeaders({ installations: [], page: { nextPageToken: "" } })),
  );
}

async function runTask(page: Page) {
  await page.getByLabel("Agent goal").fill("Adaptive fixture goal");
  await page.getByRole("button", { name: "Run task" }).click();
  await expect(page.getByText("Task finished by the fake harness")).toBeVisible();
}

test("captures the expanded and fold-fallback desktop states", async ({ page }) => {
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(180_000);
  await installFixtures(page);

  // Expanded desktop: the classic free-window shell, unchanged by the
  // adaptive slice.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.locator(".project-card.active")).toContainText("Fixture Project");
  await runTask(page);
  await page.screenshot({ path: `${captureDir}/expanded--desktop--1440x900.png` });

  // Compact phone: fresh mount at the phone viewport. The adaptive shell
  // shows the single-pane home plus the bottom nav (the pre-change build
  // only rendered the squeezed desktop; its before/ frame was captured
  // from the baseline bundle).
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.getByTestId("adaptive-bottom-nav")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("heading", { level: 1, name: "Fixture Project" })).toBeVisible();
  await page.screenshot({ path: `${captureDir}/compact--home--390x844.png` });
  if (await page.getByTestId("nav-agent").isVisible()) {
    await page.getByTestId("nav-agent").click();
    await runTask(page);
    await page.screenshot({ path: `${captureDir}/compact--agent-task--390x844.png` });
    await page.getByTestId("nav-apps").click();
    await expect(page.getByText("No apps have been registered yet.")).toBeVisible();
    await page.screenshot({ path: `${captureDir}/compact--apps--390x844.png` });
    await page.getByTestId("adaptive-apps-overlay").getByRole("button", { name: "Close" }).click();
    await page.getByTestId("nav-projects").click();
    await expect(page.getByRole("dialog", { name: "Projects" })).toBeVisible();
    await page.screenshot({ path: `${captureDir}/compact--project-sheet--390x844.png` });
  }

  // Medium tablet: fresh mount at the tablet viewport.
  await page.setViewportSize({ width: 820, height: 1180 });
  await page.reload();
  await expect(page.getByTestId("open-agent-slideover")).toBeVisible({ timeout: 15_000 });
  await page.screenshot({ path: `${captureDir}/medium--home--820x1180.png` });
  const agentHandle = page.getByTestId("open-agent-slideover");
  if (await agentHandle.isVisible()) {
    await agentHandle.click();
    await expect(page.getByTestId("agent-slideover")).toBeVisible();
    await runTask(page);
    await page.screenshot({ path: `${captureDir}/medium--agent-slideover--820x1180.png` });
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("agent-slideover")).toBeHidden();
  }
});

test("captures the fold-separated dual-pane states", async ({ page }) => {
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(120_000);
  // A device that exposes the Window Segments Management API with two
  // side-by-side segments (injected stand-in for the real fold API, which
  // Chromium gates behind a flag).
  await page.addInitScript(() => {
    Object.defineProperty(window, "getWindowSegments", {
      configurable: true,
      value: () => [
        { x: 0, y: 0, width: 632, height: 800, top: 0, left: 0, right: 632, bottom: 800 },
        { x: 648, y: 0, width: 632, height: 800, top: 0, left: 648, right: 1280, bottom: 800 },
      ],
    });
  });
  await installFixtures(page);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto("/");
  // Dual pane is the default fold posture with two segments.
  await expect(page.getByTestId("fold-pane-main")).toBeVisible({ timeout: 15_000 });
  await page.screenshot({ path: `${captureDir}/fold--dual-pane--1280x800.png` });
  await expect(page.getByTestId("fold-pane-secondary")).toBeVisible();
  await page.getByTestId("toggle-panes").click();
  await expect(page.getByTestId("fold-pane-secondary")).toBeHidden();
  await page.screenshot({ path: `${captureDir}/fold--single-pane--1280x800.png` });
});
