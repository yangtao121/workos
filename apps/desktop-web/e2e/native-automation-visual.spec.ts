import { expect, test } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";
const directory = process.env.WORKOS_NATIVE_CAPTURE_DIR;
const baseline = process.env.WORKOS_NATIVE_BASELINE === "true";
const projectId = "01999999-9999-7999-8999-000000000001";
const sessionId = "01999999-9999-7999-8999-000000000010";
for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`native automation states ${String(width)}x${String(height)}`, async ({ page }) => {
    test.skip(!directory, "explicit deterministic capture");
    await page.setViewportSize({ width, height });
    await desktopFixture(page);
    let phase = "active";
    await page.route("**/workos.*/**", async (route) => {
      const method = new URL(route.request().url()).pathname.split("/").at(-1) ?? "";
      const session = {
        id: sessionId,
        projectId,
        providerId: "deepseek",
        state: 1,
        activeTaskId: phase === "active" ? "task" : "",
        goal: {
          ref: "goal",
          revision: "3",
          objective: "Verify the calculation and prepare two independent improvements",
          phase,
          roundsStarted: 2,
          maxRounds: 8,
          armed: phase === "active",
        },
        delegations:
          phase === "children"
            ? [
                {
                  id: "alpha",
                  title: "Improve calculation coverage",
                  state: "completed",
                  baseCommit: "a".repeat(40),
                  resultSummary: "Added zero and negative input checks.",
                  resultArtifactId: "artifact",
                },
                {
                  id: "beta",
                  title: "Review error handling",
                  state: "needs_review",
                  resultSummary: "Execution was interrupted. Review its changes before continuing.",
                },
              ]
            : [],
      };
      if (phase === "children") session.goal.phase = "paused";
      const responses: Record<string, unknown> = {
        GetHarnessCatalog: {
          providers: [
            {
              id: "deepseek",
              displayName: "DeepSeek Harness",
              health: 2,
              capabilities: {
                sessionGoals: true,
                projectSkills: true,
                subagents: true,
                maxConcurrentSubagents: 2,
                maxSubagentDepth: 1,
              },
            },
          ],
        },
        ListSessions: { sessions: [session] },
        GetSession: { session },
        CreateSession: { session },
        ListProjectWorkspaces: {
          bindings: [{ id: "workspace", state: 1, displayName: "Studio source" }],
        },
        ListSessionInputs: {
          inputs: [
            {
              id: "input",
              sessionId,
              taskId: "task",
              sequence: "1",
              text: "Verify the calculation",
              state: 3,
            },
          ],
        },
        GetTask: { task: { id: "task", state: 4, providerId: "deepseek" } },
        ListTaskInteractions: { interactions: [] },
      };
      if (method.startsWith("Watch")) {
        await route.abort();
        return;
      }
      if (method in responses) {
        await route.fulfill({ json: responses[method] });
        return;
      }
      await route.fallback();
    });
    const capture = async (name: string) =>
      page.screenshot({
        path: `${directory ?? ""}/${name}--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    for (const state of ["active", "paused", "children"]) {
      phase = state;
      await page.goto("/");
      await openDesktopApp(page, "agent-sessions");
      await page
        .getByTestId("agent-sessions-app")
        .getByRole("button")
        .filter({ hasText: "Verify the calculation" })
        .click();
      await expect(page.getByTestId("agent-session-view")).toContainText("Verify the calculation");
      if (!baseline) {
        await expect(page.getByTestId("session-goal")).toBeVisible();
        if (state === "children")
          await page.getByTestId("session-delegations").scrollIntoViewIfNeeded();
        else await page.getByTestId("session-goal").scrollIntoViewIfNeeded();
      }
      await capture(`agent-session-window--native-${state}`);
    }
  });
}
