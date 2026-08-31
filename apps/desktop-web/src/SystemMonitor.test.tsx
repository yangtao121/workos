// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  IncidentSeverity,
  IncidentState,
  IncidentViolation,
  IncidentRestartOutcome,
} from "@workos/protocol";
import { SystemMonitor } from "./SystemMonitor.js";
import type { WorkOSClients } from "@workos/agent-sdk";

function incident(overrides: Record<string, unknown> = {}) {
  return {
    id: "0198d7ea-2110-7c42-b659-c5e4d73bc371",
    summary: "The app workload exited unexpectedly and was not restarted by the engine.",
    severity: IncidentSeverity.CRITICAL,
    state: IncidentState.OPEN,
    violation: IncidentViolation.UNEXPECTED_EXIT,
    restartOutcome: IncidentRestartOutcome.RESTARTED,
    acknowledgedAt: undefined,
    ...overrides,
  };
}

interface IncidentClients {
  incidents: {
    listIncidents: ReturnType<typeof vi.fn>;
    acknowledgeIncident: ReturnType<typeof vi.fn>;
  };
  appInstallations: {
    listAppVersionHistory: ReturnType<typeof vi.fn>;
    rollbackAppVersion: ReturnType<typeof vi.fn>;
  };
}

function clientsWith(
  listResult: Promise<{ incidents: Array<Record<string, unknown>> }>,
  ackResult?: Promise<Record<string, unknown>>,
  historyResult?: Promise<{ snapshots: Array<{ version: string; sequence: string }> }>,
): WorkOSClients & IncidentClients {
  const listIncidents: IncidentClients["incidents"]["listIncidents"] = vi.fn(() => listResult);
  const acknowledgeIncident: IncidentClients["incidents"]["acknowledgeIncident"] = vi.fn(
    () => ackResult ?? Promise.resolve({}),
  );
  const listAppVersionHistory: IncidentClients["appInstallations"]["listAppVersionHistory"] = vi.fn(
    () => historyResult ?? Promise.resolve({ snapshots: [] }),
  );
  const rollbackAppVersion: IncidentClients["appInstallations"]["rollbackAppVersion"] = vi.fn(() =>
    Promise.resolve({ rolledBackToVersion: "1.0.0" }),
  );
  return {
    incidents: { listIncidents, acknowledgeIncident },
    appInstallations: { listAppVersionHistory, rollbackAppVersion },
  } as unknown as WorkOSClients & IncidentClients;
}

describe("System Monitor", () => {
  afterEach(cleanup);

  it("lists the project's incidents with bounded, human-readable facts", async () => {
    const clients = clientsWith(Promise.resolve({ incidents: [incident()] }));
    render(
      <SystemMonitor expectedProjectRevision={5n} projectId="project-1" workosClients={clients} />,
    );
    await waitFor(() => {
      expect(screen.getByRole("list", { name: "Project incidents" })).toBeTruthy();
    });
    expect(screen.getByText(/exited unexpectedly/)).toBeTruthy();
    expect(screen.getByText(/Critical · Open · Unexpected exit · Restarted/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Acknowledge" })).toBeTruthy();
    const request = clients.incidents.listIncidents.mock.calls[0]?.[0] as
      | { projectId: string }
      | undefined;
    expect(request?.projectId).toBe("project-1");
  });

  it("degrades to a fixed notice when reliability is unreachable and offers retry", async () => {
    const clients = clientsWith(Promise.reject(new ConnectError("unavailable", Code.Unavailable)));
    render(
      <SystemMonitor expectedProjectRevision={5n} projectId="project-1" workosClients={clients} />,
    );
    await waitFor(() => {
      expect(screen.getByText(/temporarily unavailable/)).toBeTruthy();
    });
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
    expect(screen.queryByRole("list", { name: "Project incidents" })).toBeNull();
  });

  it("acknowledges exactly once per click and never shows a second dialog", async () => {
    const user = userEvent.setup();
    const clients = clientsWith(
      Promise.resolve({ incidents: [incident()] }),
      new Promise((resolve) => setTimeout(resolve, 20)),
    );
    render(
      <SystemMonitor expectedProjectRevision={5n} projectId="project-1" workosClients={clients} />,
    );
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Acknowledge" })).toBeTruthy();
    });
    await user.click(screen.getByRole("button", { name: "Acknowledge" }));
    expect(clients.incidents.acknowledgeIncident).toHaveBeenCalledTimes(1);
    const request = clients.incidents.acknowledgeIncident.mock.calls[0]?.[0] as
      | { incidentId: string; idempotencyKey?: string }
      | undefined;
    expect(request?.incidentId).toBe("0198d7ea-2110-7c42-b659-c5e4d73bc371");
    expect(request?.idempotencyKey).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByText("Acknowledged")).toBeTruthy();
    });
  });

  it("offers the rollback action only for eligible incidents and runs it once", async () => {
    const user = userEvent.setup();
    const clients = clientsWith(
      Promise.resolve({
        incidents: [
          incident({ appInstanceId: "01990000-0000-7000-8000-0000000000a1" }),
          incident({
            id: "01990000-0000-7000-8000-0000000000a2",
            appInstanceId: "01990000-0000-7000-8000-0000000000a1",
            state: IncidentState.RESOLVED,
          }),
        ],
      }),
      undefined,
      Promise.resolve({
        snapshots: [
          { version: "1.0.0", sequence: "1" },
          { version: "1.1.0", sequence: "2" },
        ],
      }),
    );
    render(
      <SystemMonitor expectedProjectRevision={5n} projectId="project-1" workosClients={clients} />,
    );
    // The open incident bound to an instance with history gets the action.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Roll back to 1.0.0" })).toBeTruthy();
    });
    // Resolved incidents never show an executable rollback.
    expect(screen.getAllByRole("button", { name: "Roll back to 1.0.0" }).length).toBe(1);
    await user.click(screen.getByRole("button", { name: "Roll back to 1.0.0" }));
    await waitFor(() => {
      expect(screen.getByText(/Core restored 1\.0\.0/)).toBeTruthy();
    });
    expect(clients.appInstallations.rollbackAppVersion).toHaveBeenCalledTimes(1);
    const request = clients.appInstallations.rollbackAppVersion.mock.calls[0]?.[0] as
      | { installationId: string; expectedProjectRevision: bigint }
      | undefined;
    expect(request?.installationId).toBe("01990000-0000-7000-8000-0000000000a1");
    expect(request?.expectedProjectRevision).toBe(5n);
  });

  it("shows the empty-project state without touching the API", async () => {
    const clients = clientsWith(Promise.resolve({ incidents: [] }));
    render(
      <SystemMonitor
        expectedProjectRevision={undefined}
        projectId={undefined}
        workosClients={clients}
      />,
    );
    await waitFor(() => {
      expect(screen.getByText(/Create a project to monitor/)).toBeTruthy();
    });
    expect(clients.incidents.listIncidents).not.toHaveBeenCalled();
  });
});
