import { mkdir } from "node:fs/promises";
import { createDesktopProject, openDesktopApp } from "./open-app.js";
import { expect, test } from "@playwright/test";

// The V2 development journey gate (ADR-0030 B05/B08): the real Agent
// Sessions window against a real Gateway/Core/Harness stack whose provider
// is the deterministic fake. No real model is ever called: the fake harness
// answers every task with fixed, goal-derived events.
//
// Test 1 is the fully real chain: create session -> submit input -> task
// runs on the fake provider -> assistant message and completion are visible
// -> a page refresh resumes the SAME session from the list -> a second
// input continues it.
//
// Test 2 pins the busy-state UI contract at the transport layer (the way
// the repo's fixtures mock): the task event stream is held open so the
// first input stays "running", the second submission is answered as a
// queued input, and the explicit 停止当前执行 action cancels without
// closing the session (关闭会话 stays a distinct action).
//
// Test 3 records the visual evidence (WORKOS_CAPTURE_DIR) for the agent
// session window, the Terminal/Native control surfaces, and the Running
// apps list at the repo's three standard viewports.

test.setTimeout(240_000);

test.skip(
  process.env.WORKOS_AGENT_SESSIONS_E2E !== "true",
  "requires the v2-development-journey gate stack",
);

const captureDir = process.env.WORKOS_CAPTURE_DIR ?? "";

test("agent session window runs the deterministic fake provider and resumes after refresh", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await createDesktopProject(page, `Agent Sessions ${String(Date.now())}`);

  await openDesktopApp(page, "agent-sessions");
  const app = page.getByTestId("agent-sessions-app");
  await expect(app).toBeVisible();

  await page.getByTestId("new-agent-session").click();
  const view = page.getByTestId("agent-session-view");
  await expect(view).toBeVisible();
  await expect(page.getByTestId("agent-session-provider")).toContainText("fake");

  // Round one: the input is durably accepted, dispatched as a task, and the
  // fake provider's deterministic answer renders in the timeline.
  await page.getByLabel("Session message").fill("prove the agent sessions window");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(
    page.locator('[data-testid="agent-session-transcript"] .session-input-text').first(),
  ).toContainText("prove the agent sessions window");
  await expect(
    page.locator(".agent-timeline p").filter({ hasText: "Run started · fake" }),
  ).toBeVisible({
    timeout: 60_000,
  });
  await expect(
    page.locator(".agent-timeline p").filter({ hasText: "prove the agent sessions window" }),
  ).toBeVisible();
  await expect(
    page.locator(".agent-timeline p").filter({ hasText: /completed by fake harness/ }),
  ).toBeVisible({ timeout: 60_000 });
  await expect(
    page.locator('[data-testid="agent-session-transcript"] .session-input').first(),
  ).toHaveAttribute("data-input-state", "done");

  if (captureDir) {
    await mkdir(captureDir, { recursive: true });
    await page.screenshot({
      path: `${captureDir}/agent-session-window--completed-run--1440x900.png`,
      animations: "disabled",
    });
  }

  // A reload drops every client window; the session itself is server-side
  // state, so the same session must be resumable from the list.
  await page.reload();
  await openDesktopApp(page, "agent-sessions");
  const list = page.getByTestId("agent-session-list");
  await expect(list).toBeVisible();
  const resumed = list.locator(".session-entry", { hasText: "prove the agent sessions window" });
  await expect(resumed).toBeVisible({ timeout: 30_000 });
  if (captureDir) {
    await page.screenshot({
      path: `${captureDir}/agent-sessions--list--1440x900.png`,
      animations: "disabled",
    });
  }
  await resumed.click();
  await expect(page.getByTestId("agent-session-view")).toBeVisible();
  await expect(
    page.locator('[data-testid="agent-session-transcript"] .session-input-text').first(),
  ).toContainText("prove the agent sessions window");

  // Round two on the SAME session proves the context window continues.
  await page.getByLabel("Session message").fill("second round in the same session");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(
    page
      .locator('[data-testid="agent-session-transcript"] .session-input-text')
      .filter({ hasText: "second round in the same session" }),
  ).toBeVisible();
  await expect(
    page.locator(".agent-timeline p").filter({ hasText: "second round in the same session" }),
  ).toBeVisible({ timeout: 60_000 });
  await expect(
    page.locator('[data-testid="agent-session-transcript"] .session-input').last(),
  ).toHaveAttribute("data-input-state", "done", { timeout: 60_000 });

  // The composer stays usable at the compact phone viewport.
  for (const size of [
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(page.getByLabel("Session message")).toBeVisible();
    await expect(page.getByRole("button", { name: "Send" })).toBeVisible();
    if (captureDir) {
      await page.screenshot({
        path: `${captureDir}/agent-session-window--completed-run--${String(size.width)}x${String(size.height)}.png`,
        animations: "disabled",
      });
    }
  }
});

test("busy session shows the running cancel action, queued inputs, and cancels without closing", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await createDesktopProject(page, `Agent Busy ${String(Date.now())}`);

  // Deterministic busy state: the task event stream never delivers a
  // terminal event, so the dispatched input stays "running" in the UI no
  // matter how fast the fake provider actually is.
  await page.route("**/workos.agent.v1.AgentTaskService/WatchTaskEvents*", async () => {
    await new Promise(() => undefined);
  });

  // The recorded inputs drive ListSessionInputs: the first input is pinned
  // DISPATCHED (held stream), the second is answered as ACCEPTED (queued).
  const inputs: Array<Record<string, unknown>> = [];
  let input2State = "AGENT_SESSION_INPUT_STATE_ACCEPTED";
  await page.route("**/workos.agent.v1.AgentSessionService/ListSessionInputs", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ inputs }),
    });
  });
  await page.route("**/workos.agent.v1.AgentSessionService/SubmitSessionInput", async (route) => {
    const request = (await route.request().postDataJSON()) as {
      clientInputId: string;
      text: string;
    };
    if (inputs.length === 0) {
      const response = await route.fetch();
      const body = (await response.json()) as { input?: Record<string, unknown> };
      const recorded = {
        ...(body.input ?? {}),
        clientInputId: request.clientInputId,
        text: request.text,
        state: "AGENT_SESSION_INPUT_STATE_DISPATCHED",
      };
      inputs.push(recorded);
      await route.fulfill({ response, json: { input: recorded } });
      return;
    }
    const anchor = inputs[0] ?? {};
    const anchorSessionId = typeof anchor.sessionId === "string" ? anchor.sessionId : "";
    const queued = {
      id: `input-${String(inputs.length + 1)}`,
      sessionId: anchorSessionId,
      clientInputId: request.clientInputId,
      text: request.text,
      state: "AGENT_SESSION_INPUT_STATE_ACCEPTED",
      taskId: "",
      sequence: String(inputs.length + 1),
      resultSummary: "",
    };
    inputs.push(queued);
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ input: queued }),
    });
  });
  await page.route(
    "**/workos.agent.v1.AgentSessionService/CancelSessionExecution",
    async (route) => {
      for (const input of inputs) {
        input.state = "AGENT_SESSION_INPUT_STATE_CANCELLED";
      }
      input2State = "AGENT_SESSION_INPUT_STATE_CANCELLED";
      await route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ input: inputs[0], cancelledQueued: "1" }),
      });
    },
  );

  await openDesktopApp(page, "agent-sessions");
  await page.getByTestId("new-agent-session").click();
  await expect(page.getByTestId("agent-session-view")).toBeVisible();

  await page.getByLabel("Session message").fill("first held run");
  await page.getByRole("button", { name: "Send" }).click();
  const firstRow = page.locator('[data-testid="agent-session-transcript"] .session-input').first();
  await expect(firstRow).toHaveAttribute("data-input-state", "running");
  // The running state carries the explicit stop action for the CURRENT run.
  await expect(page.getByTestId("agent-session-cancel")).toBeVisible();
  await expect(page.getByTestId("agent-session-cancel")).toContainText("停止当前执行");
  // 停止当前执行 and 关闭会话 must stay distinct, adjacent-but-separate.
  await expect(page.getByTestId("agent-session-close")).toBeVisible();
  await expect(page.getByTestId("agent-session-close")).toContainText("关闭会话");

  // A second input while the first runs is queued by the server contract.
  await page.getByLabel("Session message").fill("queued behind the run");
  await page.getByRole("button", { name: "Send" }).click();
  const secondRow = page.locator('[data-testid="agent-session-transcript"] .session-input').nth(1);
  await expect(secondRow).toHaveAttribute("data-input-state", "queued");
  await expect(page.getByTestId("agent-session-queue-hint")).toContainText(
    "1 queued input will run in order.",
  );

  // Cancelling the execution leaves the session open for further input.
  await page.getByTestId("agent-session-cancel").click();
  await expect(firstRow).toHaveAttribute("data-input-state", "cancelled");
  await expect(secondRow).toHaveAttribute("data-input-state", "cancelled");
  await expect(page.getByLabel("Session message")).toBeEnabled();
  await expect(page.getByTestId("agent-session-close")).toBeVisible();
  expect(input2State).toBe("AGENT_SESSION_INPUT_STATE_CANCELLED");
});

test("records the desktop control-surface evidence", async ({ page }) => {
  test.skip(!captureDir, "visual capture runs explicitly");
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await createDesktopProject(page, `Agent Visual ${String(Date.now())}`);

  // The launchpad baseline: a fresh project's Home (the Running apps
  // section states its honest empty verdict before any workload exists).
  await page.getByTestId("open-home").click();
  await expect(page.getByTestId("home-app")).toBeVisible();
  await expect(page.getByText("No live app sessions in this project.")).toBeVisible();
  for (const size of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(page.getByTestId("running-apps")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/home--launchpad--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  const openFromHome = async (entry: string) => {
    await page.getByTestId("open-home").click();
    const home = page.getByTestId("home-app");
    await expect(home).toBeVisible();
    await home.getByTestId(`home-entry-${entry}`).click();
  };

  // Terminal window: the Stop control is explicit; closing only detaches.
  await openFromHome("terminal");
  await expect(page.getByTestId("terminal-app")).toBeVisible();
  await expect(page.getByTestId("terminal-stop")).toBeVisible();
  await expect
    .poll(async () => page.getByTestId("terminal-output").textContent(), { timeout: 60_000 })
    .toContain("$");
  for (const size of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(page.getByTestId("terminal-stop")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/terminal-window--controls--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }

  // Running apps: the live terminal workload appears on Home with honest
  // server-derived state and open/stop actions. (Return to the expanded
  // desktop first: the compact shell has no open-home dock button.)
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByTestId("open-home").click();
  await expect(page.getByTestId("home-app")).toBeVisible();
  const running = page.getByTestId("running-apps");
  await expect(running).toBeVisible();
  await expect(running.locator(".running-app-entry").filter({ hasText: "Terminal" })).toBeVisible({
    timeout: 30_000,
  });
  for (const size of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(running).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/home--running-apps--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }

  // The Native window states the honest unavailable verdict on a runtime
  // without the X11 capture toolchain (a different deployment form from
  // the native-surface gate's streaming captures).
  await page.setViewportSize({ width: 1440, height: 900 });
  await openFromHome("native");
  await expect(page.getByTestId("native-app")).toBeVisible();
  await expect(page.getByTestId("native-verdict")).toBeVisible({ timeout: 60_000 });
  for (const size of [
    { width: 1440, height: 900 },
    { width: 820, height: 1180 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(size);
    await expect(page.getByTestId("native-verdict")).toBeVisible();
    await page.screenshot({
      path: `${captureDir}/native-window--unavailable--${String(size.width)}x${String(size.height)}.png`,
      animations: "disabled",
    });
  }
});
