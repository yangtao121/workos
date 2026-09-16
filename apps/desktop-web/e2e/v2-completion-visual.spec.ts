import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";

const directory = process.env.WORKOS_V2_CAPTURE_DIR;
const projectId = "01999999-9999-7999-8999-000000000001";
const sessionId = "01999999-9999-7999-8999-000000000010";
const taskId = "01999999-9999-7999-8999-000000000011";
const previewId = "01999999-9999-7999-8999-000000000012";
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`development states ${String(width)}x${String(height)}`, async ({ page }) => {
    test.skip(!directory, "explicit deterministic visual capture");
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    let review = false;
    await page.route("**/workos.*/**", async (route) => {
      const method = new URL(route.request().url()).pathname.split("/").at(-1) ?? "";
      const session = {
        id: sessionId,
        projectId,
        providerId: "deepseek",
        state: review ? 4 : 1,
        activeTaskId: review ? "" : taskId,
      };
      const responses: Record<string, unknown> = {
        ListProjectWorkspaces: {
          bindings: [{ id: "workspace", displayName: "Studio source", state: 1, revision: "2" }],
        },
        ListAvailableWorkspaces: {
          sources: [{ id: "source", displayName: "Studio source", kind: "local_git" }],
        },
        ListWorkspaceFiles: {
          entries: [
            { path: "calculate.cjs", kind: "file", size: "29" },
            { path: "calculate.test.cjs", kind: "file", size: "136" },
          ],
        },
        ReadWorkspaceFile: {
          content: "module.exports = n => n * 3;\n",
          etag: `sha256:${"a".repeat(64)}`,
          workspaceRevision: "2",
        },
        ListProjectWorkspacePreviews: {
          previews: [
            {
              id: previewId,
              projectId,
              state: "running",
              generation: "1",
              command: "node preview.cjs",
              port: 3000,
              url: `/previews/${previewId}/fixture/`,
            },
          ],
        },
        ListSessions: { sessions: [session] },
        CreateSession: { session },
        GetSession: { session },
        ListSessionInputs: {
          inputs: [
            {
              id: "input",
              sessionId,
              taskId,
              sequence: "1",
              text: "Update the calculation and run its tests",
              state: review ? 4 : 2,
            },
          ],
        },
        GetTask: { task: { id: taskId, state: review ? 5 : 3, providerId: "deepseek" } },
        ListTaskInteractions: {
          interactions: review
            ? []
            : [
                {
                  id: "01999999-9999-7999-8999-000000000013",
                  taskId,
                  projectId,
                  state: "pending",
                  expiresAt: "2026-09-06T09:02:00Z",
                  questions: [
                    {
                      id: "direction",
                      text: "Which result should the preview display?",
                      choices: [
                        { label: "Triple the input", description: "Use the updated calculation" },
                        { label: "Keep the existing result" },
                      ],
                    },
                  ],
                },
              ],
        },
        ListProjectSurfaces: {
          workloads: [
            {
              workloadId: "terminal",
              projectId,
              renderer: 0,
              state: "running",
              generation: "1",
              attachmentCount: 1,
              displayName: "Terminal",
            },
            {
              workloadId: "native",
              projectId,
              renderer: 4,
              state: "running",
              generation: "1",
              attachmentCount: 1,
              displayName: "Native display",
            },
          ],
        },
        AttachSurface: {
          session: { id: "native" },
          attachment: { controls: false, controlGeneration: "2" },
        },
        ReadPtySession: {
          output: btoa("$ node --test calculate.test.cjs\nok triple\n# tests 1\n# pass 1\n$ "),
          cursor: "72",
          closed: true,
        },
      };
      if (method === "WatchSessionEvents" || method === "WatchTaskEvents") {
        await route.abort();
        return;
      }
      if (method === "WriteWorkspaceFile") {
        await route.fulfill({ status: 409, json: { code: "aborted", message: "File changed" } });
        return;
      }
      if (!(method in responses)) {
        await route.fallback();
        return;
      }
      await route.fulfill({ json: responses[method] });
    });
    await page.route("**/previews/**", (route) =>
      route.fulfill({
        contentType: "text/html",
        body: "<!doctype html><html><body style='font:20px system-ui;padding:28px'><h1>Development preview</h1><p>Triple of 3 = 9</p><p>Saved in the project workspace.</p></body></html>",
      }),
    );
    const capture = async (name: string) => {
      await page.screenshot({
        path: `${directory ?? ""}/${name}--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    };
    await page.goto("/");
    await openDesktopApp(page, "home");
    await expect(page.getByTestId("running-apps")).toContainText("Native display");
    await capture("home--running-apps");
    await openDesktopApp(page, "settings");
    await expect(page.getByRole("region", { name: "Project workspace" })).toContainText(
      "Studio source",
    );
    await page.getByRole("region", { name: "Project workspace" }).scrollIntoViewIfNeeded();
    await capture("project--workspace");
    await openDesktopApp(page, "files");
    await page.getByRole("button", { name: "calculate.cjs", exact: true }).click();
    await page.getByLabel("File content").fill("module.exports = n => n * 4;\n");
    await page.getByRole("button", { name: "Save file", exact: true }).click();
    await expect(page.getByTestId("workspace-files").getByRole("alert")).toBeVisible();
    await capture("files--conflict-draft");
    await openDesktopApp(page, "Development previews");
    await page
      .getByTestId("workspace-previews")
      .getByRole("button", { name: "Open", exact: true })
      .click();
    await expect(
      page.frameLocator('iframe[title="Project development preview"]').getByRole("heading"),
    ).toHaveText("Development preview");
    await capture("workspace-preview--running");
    await openDesktopApp(page, "agent-sessions");
    await page.getByTestId("new-agent-session").click();
    await expect(page.getByRole("button", { name: "Send answer", exact: true })).toBeVisible();
    await page
      .getByRole("radio", { name: "Triple the input — Use the updated calculation", exact: true })
      .check();
    await expect(page.getByRole("button", { name: "Send answer", exact: true })).toBeEnabled();
    await page
      .getByRole("button", { name: "Reject question", exact: true })
      .scrollIntoViewIfNeeded();
    await expect(
      page.getByRole("button", { name: "Reject question", exact: true }),
    ).toBeInViewport();
    await capture("agent-session--question");
    review = true;
    await page.reload();
    await openDesktopApp(page, "agent-sessions");
    await page.getByTestId("new-agent-session").click();
    await expect(
      page.getByTestId("agent-session-view").getByRole("alert").filter({ hasText: "Inspect" }),
    ).toBeVisible();
    await capture("agent-session--needs-review");
    await openDesktopApp(page, "home");
    await page.getByTestId("home-entry-native").click();
    await expect(page.getByTestId("native-take-control")).toBeVisible();
    await capture("native-window--observer");
    await openDesktopApp(page, "home");
    await page.getByTestId("home-entry-terminal").click();
    await expect(page.getByTestId("terminal-controls-state")).toHaveText("stopped");
    await capture("terminal-window--stopped");
  });
}
