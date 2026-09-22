import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { openDesktopApp } from "./open-app.js";

test.skip(
  process.env.WORKOS_SHARED_DESKTOP_E2E !== "true",
  "requires isolated shared desktop stack",
);
const projectId = process.env.WORKOS_V2_PROJECT_ID ?? "";
const desktopService = "workos.desktop.v1.DesktopService";
const sessionService = "workos.agent.v1.AgentSessionService";
const surfaceService = "workos.surface.v1.SurfaceContinuityService";
interface Desktop {
  activeProjectId?: string;
  focusedWindowId?: string;
  revision?: string;
  windows?: {
    id: string;
    target: { kind: string; projectId?: string; workloadId?: string; sessionId?: string };
  }[];
}
async function rpc<T>(
  request: APIRequestContext,
  service: string,
  method: string,
  data: object,
): Promise<T> {
  const response = await request.post(`/${service}/${method}`, { data });
  expect(response.ok(), `${method}: ${String(response.status())}`).toBe(true);
  return (await response.json()) as T;
}
async function state(request: APIRequestContext): Promise<Desktop> {
  return (await rpc<{ state: Desktop }>(request, desktopService, "GetDesktop", {})).state;
}
async function operation(request: APIRequestContext, command: object): Promise<Desktop> {
  return (
    await rpc<{ state: Desktop }>(request, desktopService, "ApplyDesktopOperation", {
      idempotencyKey: crypto.randomUUID(),
      ...command,
    })
  ).state;
}
async function reset(request: APIRequestContext) {
  for (const window of (await state(request)).windows ?? []) {
    await operation(request, { closeWindow: { windowId: window.id } });
  }
  await operation(request, { switchProject: { projectId } });
}
async function selected(page: Page, sessionId: string) {
  await expect(page.getByTestId("agent-session-view")).toBeVisible({ timeout: 15_000 });
  await expect
    .poll(async () =>
      (await state(page.request)).windows?.some((window) => window.target.sessionId === sessionId),
    )
    .toBe(true);
}

test("real Core mirrors three devices, preserves offline drafts and continues idle conversations", async ({
  browser,
  baseURL,
}) => {
  // Separate browser processes model physical devices.
  const devices = await Promise.all([0, 1, 2].map(() => browser.browserType().launch()));
  const viewports = [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
    { width: 820, height: 1180 },
  ];
  const contexts = await Promise.all(
    devices.map((device, index) =>
      device.newContext({
        baseURL: baseURL ?? "http://127.0.0.1:8080",
        viewport: viewports[index] ?? { width: 1440, height: 900 },
        hasTouch: index > 0,
        isMobile: index === 1,
      }),
    ),
  );
  for (const context of contexts) {
    context.setDefaultTimeout(15_000);
    context.setDefaultNavigationTimeout(15_000);
  }
  try {
    const [desktop, phone, tablet] = await Promise.all(
      contexts.map((context) => context.newPage()),
    );
    if (!desktop || !phone || !tablet) throw new Error("missing contexts");
    await reset(desktop.request);
    const created = await rpc<{ session: { id: string } }>(
      desktop.request,
      sessionService,
      "CreateSession",
      { projectId, idempotencyKey: crypto.randomUUID() },
    );
    await Promise.all([desktop, phone, tablet].map((page) => page.goto("/")));
    await openDesktopApp(desktop, "agent-sessions");
    const agent = (await state(desktop.request)).windows?.find(
      (window) => window.target.kind === "agent-sessions",
    );
    expect(agent).toBeTruthy();
    await operation(desktop.request, {
      selectSession: { windowId: agent?.id, sessionId: created.session.id },
    });
    await Promise.all([desktop, phone, tablet].map((page) => selected(page, created.session.id)));
    const message = `SESSION_COUNT shared idle conversation ${browser.browserType().name()}`;
    await phone.getByLabel("Session message").fill(message);
    await phone.getByRole("button", { name: "Send", exact: true }).click();
    await Promise.all(
      [desktop, tablet].map((page) =>
        expect(page.getByTestId("agent-session-transcript")).toContainText(message, {
          timeout: 30_000,
        }),
      ),
    );
    await expect
      .poll(async () => {
        const result = await rpc<{ inputs?: { text: string }[] }>(
          desktop.request,
          sessionService,
          "ListSessionInputs",
          { sessionId: created.session.id, limit: 50 },
        );
        return result.inputs?.filter((input) => input.text === message).length;
      })
      .toBe(1);
    await Promise.all(
      [desktop, phone, tablet].map((page) =>
        expect(page.getByTestId("agent-session-transcript")).toContainText("HIST has_turn1=false", {
          timeout: 30_000,
        }),
      ),
    );
    await phone.reload();
    await selected(phone, created.session.id);
    await phone.getByLabel("Session message").fill("Local draft survives offline refresh");
    await contexts[1]?.setOffline(true);
    await phone.getByRole("button", { name: "Send", exact: true }).click();
    await expect(phone.getByLabel("Session message")).toHaveValue(
      "Local draft survives offline refresh",
    );
    await contexts[1]?.setOffline(false);
    await phone.reload();
    await expect(phone.getByLabel("Session message")).toHaveValue(
      "Local draft survives offline refresh",
    );
    await expect(desktop.getByLabel("Session message")).toHaveValue("");

    await openDesktopApp(desktop, "files");
    await expect
      .poll(async () => {
        const current = await state(desktop.request);
        return current.windows?.find((window) => window.id === current.focusedWindowId)?.target
          .kind;
      })
      .toBe("files");
    const files = (await state(desktop.request)).windows?.find(
      (window) => window.target.kind === "files",
    );
    await operation(phone.request, { closeWindow: { windowId: files?.id } });
    await expect
      .poll(async () =>
        (await state(desktop.request)).windows?.some((window) => window.id === files?.id),
      )
      .toBe(false);
    // A delayed focus cannot resurrect the now-closed window.
    const stale = await desktop.request.post(`/${desktopService}/ApplyDesktopOperation`, {
      data: { idempotencyKey: crypto.randomUUID(), focusWindow: { windowId: files?.id } },
    });
    expect(stale.status()).toBeLessThan(500);
    await expect
      .poll(async () =>
        (await state(tablet.request)).windows?.some((window) => window.id === files?.id),
      )
      .toBe(false);
  } catch (error) {
    for (const [index, context] of contexts.entries()) {
      const page = context.pages()[0];
      if (page) {
        await test.info().attach(`device-${String(index)}`, {
          body: await page.screenshot(),
          contentType: "image/png",
        });
        await test.info().attach(`device-${String(index)}-dom`, {
          body: await page.content(),
          contentType: "text/html",
        });
      }
    }
    throw error;
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await Promise.all(devices.map((device) => device.close()));
  }
});

test("real Runtime keeps exact terminal identity after every browser disconnects", async ({
  browser,
  baseURL,
}) => {
  let context = await browser.newContext({
    baseURL: baseURL ?? "http://127.0.0.1:8080",
    viewport: { width: 1440, height: 900 },
  });
  let page = await context.newPage();
  let workloadId = "";
  try {
    await reset(page.request);
    await page.goto("/");
    await openDesktopApp(page, "terminal");
    await expect
      .poll(async () => {
        const result = await state(page.request);
        workloadId =
          result.windows?.find((window) => window.target.kind === "terminal")?.target.workloadId ??
          "";
        return workloadId;
      })
      .not.toBe("");
    const read = () =>
      rpc<{
        workload: {
          workloadId: string;
          generation: string;
          state: string;
          policy: { lifecycleMode: string };
        };
      }>(page.request, surfaceService, "GetSurfaceWorkload", { workloadId });
    const before = (await read()).workload;
    expect(before.policy.lifecycleMode).toBe("LIFECYCLE_MODE_MANUAL_STOP");
    expect(before.state).toBe("running");
    await context.close();
    context = await browser.newContext({
      baseURL: baseURL ?? "http://127.0.0.1:8080",
      viewport: { width: 390, height: 844 },
    });
    page = await context.newPage();
    await page.goto("/");
    await expect
      .poll(
        async () =>
          (await state(page.request)).windows?.find((window) => window.target.kind === "terminal")
            ?.target.workloadId,
      )
      .toBe(workloadId);
    const after = (await read()).workload;
    expect(after.workloadId).toBe(before.workloadId);
    expect(after.generation).toBe(before.generation);
    expect(after.state).toBe("running");
    await rpc(page.request, surfaceService, "StopSurfaceWorkload", {
      workloadId,
      actionKey: crypto.randomUUID(),
    });
    await expect.poll(async () => (await read()).workload.state).not.toBe("running");
    await page.reload();
    expect((await read()).workload.generation).toBe(before.generation);
    expect((await read()).workload.state).not.toBe("running");
  } finally {
    if (workloadId)
      await page.request
        .post(`/${surfaceService}/StopSurfaceWorkload`, {
          data: { workloadId, actionKey: crypto.randomUUID() },
        })
        .catch(() => undefined);
    await context.close();
  }
});
