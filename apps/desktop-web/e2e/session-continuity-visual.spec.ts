import { expect, test } from "@playwright/test";
import { sharedDesktopFixture } from "./shared-desktop-fixture.js";

const captureDirectory = process.env.WORKOS_SESSION_CAPTURE_DIR ?? process.env.WORKOS_CAPTURE_DIR;
const baseline = process.env.WORKOS_SESSION_BASELINE === "true";
const projectId = "01999999-9999-7999-8999-000000000001";
const sessionId = "01999999-9999-7999-8999-000000000020";
const draft = "Next, compare the phone and tablet layouts.";
const prompt = "Summarize what I can continue on another device.";
const summary =
  "Your project, open windows and selected conversation stay together. Running apps remain available when you return.";

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`session continuity visual ${String(width)}x${String(height)}`, async ({
    page,
    context,
    browserName,
  }) => {
    test.skip(!captureDirectory, "explicit deterministic session capture");
    const fixture = await sharedDesktopFixture({ kind: "agent-sessions", projectId, sessionId });
    let submissionCount = 0;
    try {
      await page.setViewportSize({ width, height });
      await fixture.install(page);
      await page.route("**/workos.*/**", async (route) => {
        const method = new URL(route.request().url()).pathname.split("/").at(-1) ?? "";
        if (method === "WatchSessionEvents" || method === "WatchTaskEvents") {
          await route.abort();
          return;
        }
        if (method === "SubmitSessionInput") {
          submissionCount++;
          await route.abort("internetdisconnected");
          return;
        }
        const responses: Record<string, unknown> = {
          GetSession: {
            session: {
              id: sessionId,
              ownerUserId: "01999999-9999-7999-8999-000000000099",
              projectId,
              providerId: "fixture",
              state: 1,
              inputSequence: "1",
              lastEventSequence: "2",
            },
          },
          ListSessionInputs: {
            inputs: [
              {
                id: "01999999-9999-7999-8999-000000000021",
                sessionId,
                clientInputId: "fixture-confirmed-input",
                sequence: "1",
                text: prompt,
                state: 3,
                resultSummary: summary,
              },
            ],
          },
          ListProjectWorkspaces: {
            bindings: [
              {
                id: "01999999-9999-7999-8999-000000000022",
                state: 1,
                displayName: "Studio source",
              },
            ],
          },
          ListTaskInteractions: { interactions: [] },
        };
        if (method in responses) {
          await route.fulfill({ json: responses[method] });
          return;
        }
        await route.fallback();
      });
      await page.goto("/");
      const message = page.getByRole("textbox", { name: "Session message" });
      await expect(page.getByTestId("agent-session-view")).toBeVisible();
      await expect(page.getByText(summary, { exact: true })).toBeVisible();
      await message.fill(draft);
      await expect(message).toHaveValue(draft);
      const capture = async (state: string) => {
        await page.screenshot({
          path: `${captureDirectory ?? ""}/${browserName}/agent-session--${state}--${String(width)}x${String(height)}.png`,
          animations: "disabled",
        });
      };
      await capture("local-draft");
      await context.setOffline(true);
      await page.getByRole("button", { name: "Send", exact: true }).click();
      if (baseline) {
        // Same real interaction on the pre-change component: it clears the
        // composer and creates a failed pending item. No DOM or copy is faked.
        await expect(page.getByText("not sent", { exact: true })).toBeVisible();
        await expect(message).toHaveValue("");
      } else {
        await expect(page.getByTestId("agent-session-notice")).toHaveText(
          "You are offline. Your draft is kept on this device.",
        );
        await expect(message).toHaveValue(draft);
        expect(submissionCount).toBe(0);
      }
      await capture("offline-draft");
    } finally {
      await context.setOffline(false);
      await page.goto("about:blank");
      await fixture.close();
    }
  });
}
