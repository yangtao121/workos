import { expect, test } from "@playwright/test";

// Deterministic visual capture for the Project Agent Markdown / Diff review
// slice (docs/tasks/20260830-project-artifact-review.md). Run explicitly:
//   pnpm exec playwright test artifact-review-visual.spec.ts
// with WORKOS_CAPTURE_DIR pointing at the task's docs/ui capture folder.
//
// Every network response is fixed before navigation (ADR-0008 evidence
// rules): fixed project, fixed artifact ids/titles, fixed timestamps, fixed
// canonical content identical to the fake harness output the real
// `make test-artifact-review` gate persists. No live backend, no real user
// data, no random UUIDs or wall-clock timestamps on screen.

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "/captures";

test.use({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 1,
  locale: "en-US",
  timezoneId: "UTC",
});

const TASK_ID = "01990000-0000-7000-8000-0000000000aa";
const MARKDOWN_ID = "01990000-0000-7000-8000-0000000000d1";
const DIFF_ID = "01990000-0000-7000-8000-0000000000d2";
const FIXED_TIME = "2026-08-30T09:00:00Z";

const MARKDOWN_TEXT = `# Fake Harness Review Document

## Summary

This document is deterministic synthetic output from the fake harness.
It exists to exercise the review artifact pipeline end to end.

## Checklist

- bounded canonical content
- no hidden magic strings
- read-only review surface

## Notes

\`\`\`text
review fixtures stay synthetic
\`\`\`
`;

const DIFF_TEXT = `diff --git a/src/example.ts b/src/example.ts
--- a/src/example.ts
+++ b/src/example.ts
@@ -1,4 +1,5 @@
 const greeting = "hello";
-const target = "world";
+const target = "workos";
+// synthetic review change
 export function greet(): string {
   return greeting + ", " + target;
 }
`;

const artifactJson = (
  id: string,
  type: string,
  title: string,
  mediaType: string,
  size: number,
) => ({
  id,
  projectId: "01990000-0000-7000-8000-000000000001",
  type,
  title,
  mediaType,
  contentRef: "",
  digest: `sha256:${"a".repeat(64)}`,
  createdAt: FIXED_TIME,
  totalSizeBytes: String(size),
  fileCount: 1,
  sourceTaskId: TASK_ID,
});

function jsonHeaders(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

// connectFrame encodes one Connect streaming envelope: 1 zero byte (no
// compression) + 4 big-endian length bytes + the JSON payload.
function connectFrames(messages: unknown[]): Buffer {
  const frames = messages.map((message) => {
    const payload = Buffer.from(JSON.stringify(message), "utf8");
    const header = Buffer.alloc(5);
    header.writeUInt32BE(payload.length, 1);
    return Buffer.concat([header, payload]);
  });
  // The Connect protocol ends a server stream with a dedicated EndStream
  // response frame; without it the client treats the stream as broken.
  const end = Buffer.from(JSON.stringify({ metadata: {} }), "utf8");
  const endHeader = Buffer.alloc(5);
  endHeader.writeUInt8(2, 0); // EndStreamResponse flag bit
  endHeader.writeUInt32BE(end.length, 1);
  frames.push(Buffer.concat([endHeader, end]));
  return Buffer.concat(frames);
}

const timelineEvents = [
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e1",
      taskId: TASK_ID,
      sequence: "1",
      runStarted: { runId: "01990000-0000-7000-8000-0000000000f1", providerId: "fake" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e2",
      taskId: TASK_ID,
      sequence: "2",
      assistantMessage: { text: "Fake harness received: Synthetic review goal" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e3",
      taskId: TASK_ID,
      sequence: "3",
      artifactCreated: { artifactId: MARKDOWN_ID, artifactType: "document.markdown.v1" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e4",
      taskId: TASK_ID,
      sequence: "4",
      artifactCreated: { artifactId: DIFF_ID, artifactType: "code.unified-diff.v1" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e5",
      taskId: TASK_ID,
      sequence: "5",
      usageRecorded: { inputTokens: "24", outputTokens: "4", model: "fake/deterministic" },
    },
  },
  {
    event: {
      id: "01990000-0000-7000-8000-0000000000e6",
      taskId: TASK_ID,
      sequence: "6",
      runCompleted: { summary: "Task finished by the fake harness" },
    },
  },
];

test("captures the artifact review surfaces", async ({ page }) => {
  test.skip(!process.env.WORKOS_CAPTURE_DIR, "visual capture runs explicitly");
  test.setTimeout(120_000);

  // Fixed device session: the AuthGate restores straight into the Desktop.
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
              structuredArtifacts: true,
              supportedArtifactTypes: ["document.markdown.v1", "code.unified-diff.v1"],
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
          lastEventSequence: "6",
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
          lastEventSequence: "6",
        },
      }),
    ),
  );

  await page.goto("/");
  await expect(page.locator(".project-card.active")).toContainText("Fixture Project");
  await page.getByLabel("Agent goal").fill("Synthetic review goal");
  await page.getByRole("checkbox", { name: "Markdown document" }).check();
  await page.getByRole("checkbox", { name: "Unified diff" }).check();
  await page.getByRole("button", { name: "Run task" }).click();
  const artifactEvents = page.locator('li[data-event="artifactCreated"]');
  await expect(artifactEvents).toHaveCount(2);
  await expect(page.getByText("Task finished by the fake harness")).toBeVisible();

  // 1. Agent Timeline with Core-minted, clickable artifact events. The
  // timeline area scrolls; bring the second artifact row into the frame so
  // both Core-minted references are visible.
  await artifactEvents.nth(1).scrollIntoViewIfNeeded();
  await page.screenshot({ path: `${captureDir}/agent-center--artifact-created--1440x900.png` });

  // Fixed review reads: first markdown, then diff.
  let reviewCall = 0;
  await page.route("**/workos.artifact.v1.ArtifactService/GetReviewArtifact", (route) => {
    reviewCall += 1;
    const markdown = reviewCall === 1;
    const artifact = markdown
      ? artifactJson(
          MARKDOWN_ID,
          "document.markdown.v1",
          "Fake Harness Review Document",
          "text/markdown; charset=utf-8",
          MARKDOWN_TEXT.length,
        )
      : artifactJson(
          DIFF_ID,
          "code.unified-diff.v1",
          "Fake Harness Proposed Patch",
          "text/x-diff; charset=utf-8",
          DIFF_TEXT.length,
        );
    const content = markdown
      ? {
          markdown: {
            mediaType: "text/markdown; charset=utf-8",
            content: Buffer.from(MARKDOWN_TEXT, "utf8").toString("base64"),
          },
        }
      : {
          unifiedDiff: {
            mediaType: "text/x-diff; charset=utf-8",
            content: Buffer.from(DIFF_TEXT, "utf8").toString("base64"),
          },
        };
    return route.fulfill(jsonHeaders({ artifact, content }));
  });

  // 2. Markdown review window.
  await artifactEvents
    .first()
    .getByRole("button", { name: /Open review/ })
    .click();
  await expect(
    page
      .locator(".artifact-viewer-body")
      .getByRole("heading", { level: 1, name: "Fake Harness Review Document" }),
  ).toBeVisible();
  await page.screenshot({ path: `${captureDir}/artifact-viewer--markdown-review--1440x900.png` });
  await page.getByRole("button", { name: "Close Artifact Review" }).last().click();

  // 3. Unified diff review window.
  await artifactEvents
    .nth(1)
    .getByRole("button", { name: /Open review/ })
    .click();
  await expect(
    page.locator(".artifact-viewer-body").getByText("diff --git a/src/example.ts b/src/example.ts"),
  ).toBeVisible();
  await page.screenshot({
    path: `${captureDir}/artifact-viewer--unified-diff-review--1440x900.png`,
  });
  await page.getByRole("button", { name: "Close Artifact Review" }).last().click();

  // 4. Artifact Center listing the project's artifacts.
  await page.route("**/workos.artifact.v1.ArtifactService/ListArtifacts", (route) =>
    route.fulfill(
      jsonHeaders({
        artifacts: [
          artifactJson(
            MARKDOWN_ID,
            "document.markdown.v1",
            "Fake Harness Review Document",
            "text/markdown; charset=utf-8",
            MARKDOWN_TEXT.length,
          ),
          artifactJson(
            DIFF_ID,
            "code.unified-diff.v1",
            "Fake Harness Proposed Patch",
            "text/x-diff; charset=utf-8",
            DIFF_TEXT.length,
          ),
        ],
        page: { nextPageToken: "" },
      }),
    ),
  );
  await page.getByRole("button", { name: "Open Artifact Center" }).click();
  await expect(page.getByTestId("artifact-row")).toHaveCount(2);
  await page.screenshot({ path: `${captureDir}/artifact-center--project-list--1440x900.png` });
});
