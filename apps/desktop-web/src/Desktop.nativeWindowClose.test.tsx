// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import {
  DesktopStateSchema,
  GreenfieldWindowSnapshotSchema,
  type DesktopState,
} from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GreenfieldViewerState } from "./NativeApp.js";
import { Desktop } from "./Desktop.js";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let viewer: GreenfieldViewerState;
let publishViewer: ((viewer?: GreenfieldViewerState) => void) | undefined;

vi.mock("./NativeApp.js", async () => {
  const React = await import("react");
  return {
    NativeApp: ({ onGreenfieldViewer }: { onGreenfieldViewer?: typeof publishViewer }) => {
      React.useEffect(() => {
        publishViewer = onGreenfieldViewer;
        onGreenfieldViewer?.(viewer);
      }, []);
      return React.createElement("div", { "data-testid": "native-app" });
    },
  };
});
vi.mock("./GreenfieldWindowApp.js", async () => {
  const React = await import("react");
  return {
    GreenfieldWindowApp: () => React.createElement("div", { "data-testid": "greenfield-window" }),
  };
});

const projectId = "01999999-9999-7999-8999-000000000111";
const workloadId = "01999999-9999-7999-8999-000000000112";
const anchorId = "01999999-9999-7999-8999-000000000113";
const mainId = "01999999-9999-7999-8999-000000000114";
const dialogId = "01999999-9999-7999-8999-000000000115";

function snapshot(withDialog = true) {
  return create(GreenfieldWindowSnapshotSchema, {
    sessionId: workloadId,
    workloadGeneration: 1n,
    revision: withDialog ? 1n : 2n,
    windows: [
      {
        id: mainId,
        title: "Code",
        appId: "code",
        zOrder: 1,
        active: true,
        revision: 1n,
        contentRect: { x: 10, y: 10, width: 700, height: 500 },
        visualRect: { x: 10, y: 10, width: 700, height: 500 },
      },
      ...(withDialog
        ? [
            {
              id: dialogId,
              parentWindowId: mainId,
              title: "Open File",
              appId: "code",
              zOrder: 2,
              active: false,
              revision: 1n,
              contentRect: { x: 110, y: 90, width: 350, height: 220 },
              visualRect: { x: 110, y: 90, width: 350, height: 220 },
            },
          ]
        : []),
    ],
  });
}

function fixture(canControl = true) {
  let desktopState: DesktopState = create(DesktopStateSchema, {
    activeProjectId: projectId,
    focusedWindowId: anchorId,
    revision: 1n,
    windows: [
      {
        id: anchorId,
        target: {
          kind: "native",
          projectId,
          resource: { case: "workloadId", value: workloadId },
          expectedWorkloadId: workloadId,
          expectedWorkloadGeneration: 1n,
        },
      },
    ],
  });
  const send = vi.fn(() => Promise.resolve());
  viewer = {
    attachment: {
      sessionId: workloadId,
      attachmentId: "01999999-9999-7999-8999-000000000116",
      workloadGeneration: 1n,
      controlGeneration: 1n,
      controls: canControl,
    },
    input: { canControl, send } as unknown as GreenfieldViewerState["input"],
    projection: { connection: "connected", epoch: 1, snapshot: snapshot() },
    requestControl: vi.fn(() => Promise.resolve()),
  };
  const closeNativeSession = vi.fn();
  const stopSurfaceWorkload = vi.fn();
  const applyDesktopOperation = vi.fn((request: { operation: { case: string } }) => {
    if (request.operation.case === "closeWindow") {
      desktopState = create(DesktopStateSchema, {
        ...desktopState,
        revision: desktopState.revision + 1n,
        windows: [],
        focusedWindowId: "",
      });
    }
    return Promise.resolve({ state: desktopState });
  });
  const clients = {
    desktop: {
      getDesktop: vi.fn(() => Promise.resolve({ state: desktopState })),
      applyDesktopOperation,
      watchDesktop: async function* (_: unknown, options: { signal?: AbortSignal }) {
        await new Promise<void>((resolve) => {
          options.signal?.addEventListener("abort", () => resolve(), { once: true });
        });
      },
    },
    projects: {
      listProjects: vi.fn(() =>
        Promise.resolve({
          projects: [
            {
              id: projectId,
              name: "Fixture project",
              revision: 1n,
              workspaceRefs: [],
              installedAppIds: [],
            },
          ],
          page: { nextPageToken: "" },
        }),
      ),
      getProject: vi.fn(),
    },
    harnessCatalog: { getHarnessCatalog: vi.fn(() => Promise.resolve({ providers: [] })) },
    appRegistry: { listApps: vi.fn(() => Promise.resolve({ apps: [], page: {} })) },
    appInstallations: { listInstalledApps: vi.fn(() => Promise.resolve({ installations: [] })) },
    surfaces: { closeSurface: vi.fn() },
    nativeSessions: { closeNativeSession },
    surfaceContinuity: { stopSurfaceWorkload },
  } as unknown as WorkOSClients;
  return { clients, send, applyDesktopOperation, closeNativeSession, stopSurfaceWorkload };
}

afterEach(() => {
  cleanup();
  publishViewer = undefined;
  window.sessionStorage.clear();
});

describe("Desktop resident window close routing", () => {
  it("requests exact child close and waits for Runtime removal without detaching Code", async () => {
    const f = fixture();
    render(<Desktop workosClients={f.clients} />);
    const dialogClose = await screen.findByRole<HTMLButtonElement>("button", {
      name: "Close Open File",
    });
    await userEvent.click(dialogClose);
    await waitFor(() => {
      expect(f.send).toHaveBeenCalledWith(dialogId, [{ case: "close", value: {} }]);
    });
    expect(dialogClose.disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Close Code" })).toBeTruthy();
    expect(f.applyDesktopOperation).not.toHaveBeenCalledWith(
      expect.objectContaining({ operation: { case: "closeWindow" } }),
    );
    expect(f.closeNativeSession).not.toHaveBeenCalled();
    expect(f.stopSurfaceWorkload).not.toHaveBeenCalled();

    await act(async () => {
      publishViewer?.({
        ...viewer,
        projection: { ...viewer.projection, snapshot: snapshot(false) },
      });
    });
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Close Open File" })).toBeNull(),
    );
    expect(screen.getByRole("button", { name: "Close Code" })).toBeTruthy();
  });

  it("closes the Core anchor for the top level without sending native Close or Stop", async () => {
    const f = fixture();
    render(<Desktop workosClients={f.clients} />);
    await userEvent.click(await screen.findByRole("button", { name: "Close Code" }));
    await waitFor(() => {
      expect(f.applyDesktopOperation).toHaveBeenCalledWith(
        expect.objectContaining({
          operation: { case: "closeWindow", value: { windowId: anchorId } },
        }),
      );
    });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close Code" })).toBeNull());
    expect(f.send).not.toHaveBeenCalledWith(mainId, [{ case: "close", value: {} }]);
    expect(f.closeNativeSession).not.toHaveBeenCalled();
    expect(f.stopSurfaceWorkload).not.toHaveBeenCalled();
  });

  it("keeps an observer's dialog visible and reports that close needs control", async () => {
    const f = fixture(false);
    render(<Desktop workosClients={f.clients} />);
    await userEvent.click(await screen.findByRole("button", { name: "Close Open File" }));
    expect(screen.getByRole("alert").textContent).toContain("原生弹窗关闭不可用");
    expect(screen.getByRole("button", { name: "Close Open File" })).toBeTruthy();
    expect(f.send).not.toHaveBeenCalled();
    expect(f.applyDesktopOperation).not.toHaveBeenCalledWith(
      expect.objectContaining({ operation: { case: "closeWindow" } }),
    );
  });
});
