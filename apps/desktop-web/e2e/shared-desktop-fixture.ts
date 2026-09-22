import { createServer, type ServerResponse } from "node:http";
import type { Page } from "@playwright/test";
import { desktopFixture } from "./desktop-fixture.js";
const projectId = "01999999-9999-7999-8999-000000000001";
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
export async function sharedDesktopFixture(initialTarget: Target = { kind: "home" }) {
  let counter = 10;
  let state: State = {
    activeProjectId: projectId,
    windows: [{ id: "01999999-9999-7999-8999-000000000010", target: initialTarget }],
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
