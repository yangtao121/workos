// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WorkOSClients } from "@workos/agent-sdk";
import { RunningSurfaces } from "./RunningSurfaces.js";

afterEach(cleanup);

function fixture(workloads: unknown[]) {
  const listProjectSurfaces = vi.fn(() => Promise.resolve({ workloads }));
  const stopSurfaceWorkload = vi.fn(() => Promise.resolve({}));
  const clients = {
    surfaceContinuity: { listProjectSurfaces, stopSurfaceWorkload },
  } as unknown as WorkOSClients;
  return {
    listProjectSurfaces,
    stopSurfaceWorkload,
    clients,
    openTerminal: vi.fn(),
    openNative: vi.fn(),
    openAppInstance: vi.fn(),
  };
}

function element(f: ReturnType<typeof fixture>) {
  return (
    <RunningSurfaces
      projectId="project-1"
      workosClients={f.clients}
      onOpenTerminal={f.openTerminal}
      onOpenNative={f.openNative}
      onOpenAppInstance={f.openAppInstance}
    />
  );
}

describe("Running apps list", () => {
  it("renders server-derived workload facts with open and stop actions", async () => {
    const f = fixture([
      {
        workloadId: "workload-terminal",
        projectId: "project-1",
        appInstanceId: "",
        renderer: 0,
        displayName: "Terminal",
        state: "running",
        attachmentCount: 2,
      },
      {
        workloadId: "workload-native",
        projectId: "project-1",
        appInstanceId: "",
        renderer: 4,
        displayName: "Native display",
        state: "running",
        attachmentCount: 0,
      },
      {
        workloadId: "workload-app",
        projectId: "project-1",
        appInstanceId: "app-instance-1",
        renderer: 2,
        displayName: "Fixture web app",
        state: "running",
        attachmentCount: 1,
      },
    ]);
    render(element(f));
    expect(await screen.findByText("Terminal")).toBeTruthy();
    expect(screen.getByText("Native display")).toBeTruthy();
    expect(screen.getByText("Fixture web app")).toBeTruthy();
    expect(screen.getByText("2 attached")).toBeTruthy();

    const rows = screen.getAllByRole("listitem");
    await userEvent.click(withinRow(rows[0] as HTMLElement, "Open"));
    expect(f.openTerminal).toHaveBeenCalledOnce();
    await userEvent.click(withinRow(rows[1] as HTMLElement, "Open"));
    expect(f.openNative).toHaveBeenCalledOnce();
    await userEvent.click(withinRow(rows[2] as HTMLElement, "Open"));
    expect(f.openAppInstance).toHaveBeenCalledWith("app-instance-1");

    await userEvent.click(withinRow(rows[0] as HTMLElement, "Stop"));
    await waitFor(() => {
      expect(f.stopSurfaceWorkload).toHaveBeenCalledWith({
        workloadId: "workload-terminal",
        actionKey: expect.any(String) as string,
      });
    });
  });

  it("states the honest verdicts for unavailable and empty lists", async () => {
    const f = fixture([]);
    f.listProjectSurfaces.mockRejectedValue(new Error("runtime down"));
    render(element(f));
    expect(
      await screen.findByText("Live app sessions are unavailable in this deployment."),
    ).toBeTruthy();

    const empty = fixture([]);
    render(element(empty));
    expect(await screen.findByText("No live app sessions in this project.")).toBeTruthy();
  });
});

function withinRow(row: HTMLElement, name: string): HTMLElement {
  return within(row).getByRole("button", { name });
}
