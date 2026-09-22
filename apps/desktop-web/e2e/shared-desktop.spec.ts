import { createServer, type ServerResponse } from "node:http";
import { expect, test, type Page } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
import { openDesktopApp } from "./open-app.js";

const projectId = "01999999-9999-7999-8999-000000000001";
const secondProject = "01999999-9999-7999-8999-000000000002";
interface Target {
  kind: string;
  projectId?: string;
  sessionId?: string;
  workloadId?: string;
  previewId?: string;
}
interface WindowRef {
  id: string;
  target: Target;
}
interface State {
  activeProjectId: string;
  windows: WindowRef[];
  focusedWindowId: string;
  revision: string;
}
function frame(value: unknown) {
  const payload = Buffer.from(JSON.stringify(value));
  const header = Buffer.alloc(5);
  header.writeUInt32BE(payload.length, 1);
  return Buffer.concat([header, payload]);
}
// A deterministic canonical service fixture with real HTTP streaming. This
// exercises Connect framing and multiple independent profiles without charging
// a provider; the integrated stack gate separately proves Core persistence.
async function fixtureServer() {
  let counter = 10;
  let state: State = {
    activeProjectId: projectId,
    windows: [{ id: "01999999-9999-7999-8999-000000000010", target: { kind: "home" } }],
    focusedWindowId: "01999999-9999-7999-8999-000000000010",
    revision: "1",
  };
  const streams = new Set<ServerResponse>();
  const operations: unknown[] = [];
  const server = createServer((request, response) => {
    response.setHeader("Access-Control-Allow-Origin", request.headers.origin ?? "*");
    response.setHeader("Access-Control-Allow-Credentials", "true");
    response.setHeader(
      "Access-Control-Allow-Headers",
      "content-type,connect-protocol-version,connect-timeout-ms,x-user-agent",
    );
    if (request.method === "OPTIONS") {
      response.end();
      return;
    }
    const method = request.url?.split("/").at(-1);
    if (method === "WatchDesktop") {
      response.setHeader("Content-Type", "application/connect+json");
      response.flushHeaders();
      streams.add(response);
      response.write(frame({ state }));
      const heartbeat = setInterval(() => response.write(frame({ heartbeat: true })), 1000);
      response.on("close", () => {
        clearInterval(heartbeat);
        streams.delete(response);
      });
      return;
    }
    let body = "";
    request.on("data", (chunk: Buffer) => {
      body += chunk.toString();
    });
    request.on("end", () => {
      if (method === "ApplyDesktopOperation") {
        const operation = JSON.parse(body) as {
          switchProject?: { projectId: string };
          openWindow?: Target;
          closeWindow?: { windowId: string };
          focusWindow?: { windowId: string };
          selectSession?: { windowId: string; sessionId: string };
        };
        operations.push(operation);
        if (operation.switchProject) {
          state.activeProjectId = operation.switchProject.projectId;
          state.focusedWindowId = "";
        }
        if (operation.openWindow) {
          const target = operation.openWindow;
          let existing = state.windows.find(
            (item) =>
              item.target.kind === target.kind && item.target.projectId === target.projectId,
          );
          if (!existing) {
            existing = {
              id: `01999999-9999-7999-8999-${String(++counter).padStart(12, "0")}`,
              target,
            };
            state.windows.push(existing);
          }
          state.windows = [...state.windows.filter((item) => item !== existing), existing];
          state.focusedWindowId = existing.id;
        }
        if (operation.closeWindow) {
          state.windows = state.windows.filter(
            (item) => item.id !== operation.closeWindow?.windowId,
          );
          state.focusedWindowId = state.windows.at(-1)?.id ?? "";
        }
        if (operation.focusWindow) {
          const existing = state.windows.find(
            (item) => item.id === operation.focusWindow?.windowId,
          );
          if (existing) {
            state.windows = [...state.windows.filter((item) => item !== existing), existing];
            state.focusedWindowId = existing.id;
          }
        }
        if (operation.selectSession) {
          const existing = state.windows.find(
            (item) => item.id === operation.selectSession?.windowId,
          );
          if (existing) existing.target.sessionId = operation.selectSession.sessionId;
        }
        state = { ...state, revision: String(Number(state.revision) + 1) };
        for (const stream of streams) stream.write(frame({ state }));
      }
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify({ state }));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "0.0.0.0", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("fixture address missing");
  return {
    operations,
    state: () => state,
    async install(page: Page) {
      await desktopFixture(page);
      await page.route("**/workos.desktop.v1.DesktopService/**", (route) =>
        route.continue({
          url: `http://127.0.0.1:${String(address.port)}${new URL(route.request().url()).pathname}`,
        }),
      );
      await page.route("**/ListProjectSurfaces", (route) =>
        route.fulfill({ json: { workloads: [] } }),
      );
      await page.route("**/ListSessions", (route) => route.fulfill({ json: { sessions: [] } }));
    },
    async close() {
      for (const stream of streams) stream.end();
      server.closeAllConnections();
      await new Promise<void>((resolve) =>
        server.close(() => {
          resolve();
        }),
      );
    },
  };
}

test("three independent screen sizes share project, windows, focus and reload authority", async ({
  browser,
}) => {
  const fixture = await fixtureServer();
  const contexts = await Promise.all(
    [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
      { width: 820, height: 1180 },
    ].map((viewport) => browser.newContext({ viewport })),
  );
  try {
    const pages = await Promise.all(contexts.map((context) => context.newPage()));
    for (const page of pages) {
      await fixture.install(page);
      await page.goto("/");
      await expect(page.getByTestId("home-app")).toBeVisible();
    }
    const [desktop, phone, tablet] = pages;
    if (!desktop || !phone || !tablet) throw new Error("fixture pages missing");
    await openDesktopApp(desktop, "files");
    for (const page of pages) await expect(page.getByTestId("workspace-files")).toBeVisible();
    await phone.getByTestId("nav-agent").click();
    for (const page of pages)
      await expect(
        page.getByRole("heading", { name: "Agent Sessions", exact: true }),
      ).toBeVisible();
    await tablet.getByRole("button", { name: "Close Agent Sessions", exact: true }).click();
    for (const page of pages)
      await expect(page.getByRole("heading", { name: "Agent Sessions", exact: true })).toHaveCount(
        0,
      );
    await phone.reload();
    await expect(phone.getByTestId("workspace-files")).toBeVisible();
    await desktop.keyboard.press("ControlOrMeta+k");
    await desktop.getByLabel("Search commands").fill("Switch to project: Field notes");
    await desktop.keyboard.press("Enter");
    for (const page of pages)
      await expect(page.getByRole("button", { name: "Switch project", exact: true })).toContainText(
        "Field notes",
      );
    expect(fixture.state().activeProjectId).toBe(secondProject);
    expect(
      fixture
        .state()
        .windows.some(
          (item) => item.target.kind === "files" && item.target.projectId === projectId,
        ),
    ).toBe(true);
  } finally {
    await Promise.all(contexts.map((context) => context.close()));
    await fixture.close();
  }
});

for (const [width, height] of [
  [1440, 900],
  [820, 1180],
  [390, 844],
] as const) {
  test(`shared desktop visual ${String(width)}x${String(height)}`, async ({ page }) => {
    const directory = process.env.WORKOS_CAPTURE_DIR;
    test.skip(!directory, "explicit deterministic visual capture");
    const fixture = await fixtureServer();
    try {
      await page.setViewportSize({ width, height });
      await fixture.install(page);
      await page.goto("/");
      await expect(page.getByTestId("home-app")).toBeVisible();
      await expect(page.getByText("No live app sessions in this project.")).toBeVisible();
      await page.screenshot({
        path: `${directory ?? ""}/home--launchpad--${String(width)}x${String(height)}.png`,
        animations: "disabled",
      });
    } finally {
      await page.goto("about:blank");
      await fixture.close();
    }
  });
}
