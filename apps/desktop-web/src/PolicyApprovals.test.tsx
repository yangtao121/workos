// @vitest-environment jsdom

import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WorkOSClients } from "@workos/agent-sdk";
import type {
  AppAgentPolicy,
  AppInstallation,
  AgentApproval,
  GetAppPolicyResponse,
  GetAppUsageResponse,
  ListApprovalsResponse,
} from "@workos/protocol";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApprovalsView } from "./ApprovalsView.js";
import { PolicyDialog } from "./PolicyDialog.js";
import { UsageView } from "./UsageView.js";

// React 19 act() requires this flag in jsdom to flush deferred promise updates.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(cleanup);

const approval = (overrides: Partial<AgentApproval> = {}): AgentApproval => ({
  $typeName: "workos.agent.v1.AgentApproval",
  approvalId: "018f0000-0000-7000-8000-0000000000aa",
  taskId: "018f0000-0000-7000-8000-0000000000bb",
  projectId: "018f0000-0000-7000-8000-0000000000cc",
  installationId: "018f0000-0000-7000-8000-0000000000dd",
  appId: "fixture.app",
  goalExcerpt: "Summarize the fixture",
  providerId: "fake",
  maxOutputTokensPerTask: 256n,
  maxRuntimeSecondsPerTask: 60n,
  policyRevision: 1n,
  state: 1,
  decidedAt: undefined,
  createdAt: { $typeName: "google.protobuf.Timestamp", seconds: 1788000000n, nanos: 0 },
  ...overrides,
});

const policy = (overrides: Partial<AppAgentPolicy> = {}): AppAgentPolicy => ({
  $typeName: "workos.agent.v1.AppAgentPolicy",
  projectId: "018f0000-0000-7000-8000-0000000000cc",
  installationId: "018f0000-0000-7000-8000-0000000000dd",
  spec: {
    $typeName: "workos.agent.v1.AppAgentPolicySpec",
    executionMode: 1,
    maxOutputTokensPerTask: 4096n,
    maxRuntimeSecondsPerTask: 120n,
    maxTasksPerUtcDay: 50n,
    maxReservedOutputTokensPerUtcDay: 204800n,
  },
  source: 1,
  policyRevision: 1n,
  ...overrides,
});

function approvalsFixture(
  listApprovals: ReturnType<typeof vi.fn>,
  decideApproval?: ReturnType<typeof vi.fn>,
): WorkOSClients {
  return {
    approvals: { listApprovals, decideApproval: decideApproval ?? vi.fn() },
  } as unknown as WorkOSClients;
}

describe("ApprovalsView", () => {
  it("renders pending approvals and approves them", async () => {
    const user = userEvent.setup();
    const listApprovals = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.ListApprovalsResponse",
        approvals: [approval()],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListApprovalsResponse),
    );
    const decideApproval = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.DecideApprovalResponse",
        approval: approval({ state: 2 }),
      }),
    );
    render(
      <ApprovalsView
        projectId="018f0000-0000-7000-8000-0000000000cc"
        workosClients={approvalsFixture(listApprovals, decideApproval)}
      />,
    );
    expect(await screen.findByText("Summarize the fixture")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => {
      expect(decideApproval).toHaveBeenCalledTimes(1);
    });
    const decideCalls = (decideApproval as ReturnType<typeof vi.fn>).mock.calls as Array<
      [{ decision: number }]
    >;
    expect(decideCalls[0]?.[0]?.decision).toBe(1);
    expect(await screen.findByText(/approved and queued/)).toBeTruthy();
  });

  it("maps a lost race to a stable conflict message", async () => {
    const user = userEvent.setup();
    const listApprovals = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.ListApprovalsResponse",
        approvals: [approval()],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListApprovalsResponse),
    );
    const decideApproval = vi.fn(() => Promise.reject(new ConnectError("decided", Code.Aborted)));
    render(
      <ApprovalsView
        projectId="018f0000-0000-7000-8000-0000000000cc"
        workosClients={approvalsFixture(listApprovals, decideApproval)}
      />,
    );
    await screen.findByText("Summarize the fixture");
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText(/already decided elsewhere/)).toBeTruthy();
  });

  it("keeps quota exhaustion and staleness honest", async () => {
    const listApprovals = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.ListApprovalsResponse",
        approvals: [],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      } satisfies ListApprovalsResponse),
    );
    const decideApproval = vi.fn(() =>
      Promise.reject(new ConnectError("gone", Code.FailedPrecondition)),
    );
    render(
      <ApprovalsView
        projectId="018f0000-0000-7000-8000-0000000000cc"
        workosClients={approvalsFixture(listApprovals, decideApproval)}
      />,
    );
    expect(await screen.findByText(/No tasks are waiting/)).toBeTruthy();
  });
});

function policyFixture(
  getAppPolicy: ReturnType<typeof vi.fn>,
  setAppPolicy?: ReturnType<typeof vi.fn>,
): WorkOSClients {
  return {
    appPolicies: { getAppPolicy, setAppPolicy: setAppPolicy ?? vi.fn() },
  } as unknown as WorkOSClients;
}

const installation: AppInstallation = {
  $typeName: "workos.app.v1.AppInstallation",
  id: "018f0000-0000-7000-8000-0000000000dd",
  projectId: "018f0000-0000-7000-8000-0000000000cc",
  appId: "fixture.app",
  version: "1.0.0",
  manifestDigest: `sha256:${"0".repeat(64)}`,
  installedAt: { $typeName: "google.protobuf.Timestamp", seconds: 1788000000n, nanos: 0 },
  uninstalledAt: undefined,
  grantedPermissions: ["agent.task.run"],
  grantRevision: 1n,
};

describe("PolicyDialog", () => {
  it("shows the system default and saves an explicit full replacement", async () => {
    const user = userEvent.setup();
    const getAppPolicy = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.GetAppPolicyResponse",
        policy: policy({ source: 1 }),
      } satisfies GetAppPolicyResponse),
    );
    const setAppPolicy = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.SetAppPolicyResponse",
        policy: policy({ source: 2, policyRevision: 1n }),
      }),
    );
    render(
      <PolicyDialog
        installation={installation}
        workosClients={policyFixture(getAppPolicy, setAppPolicy)}
        onClose={() => undefined}
      />,
    );
    expect(await screen.findByText(/Current: system default/)).toBeTruthy();
    await user.click(screen.getByRole("radio", { name: /Require approval/ }));
    await user.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => {
      expect(setAppPolicy).toHaveBeenCalledTimes(1);
    });
    const saveCalls = (setAppPolicy as ReturnType<typeof vi.fn>).mock.calls as Array<
      [
        {
          expectedPolicyRevision: bigint;
          spec: { executionMode: number; maxOutputTokensPerTask: bigint };
        },
      ]
    >;
    const command = saveCalls[0]?.[0];
    expect(command?.expectedPolicyRevision).toBe(0n);
    expect(command?.spec.executionMode).toBe(2);
    expect(command?.spec.maxOutputTokensPerTask).toBe(4096n);
    expect(await screen.findByText(/Agent policy saved/)).toBeTruthy();
  });

  it("rejects zero limits client-side and never calls the service", async () => {
    const user = userEvent.setup();
    const getAppPolicy = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.GetAppPolicyResponse",
        policy: policy(),
      } satisfies GetAppPolicyResponse),
    );
    const setAppPolicy = vi.fn();
    render(
      <PolicyDialog
        installation={installation}
        workosClients={policyFixture(getAppPolicy, setAppPolicy)}
        onClose={() => undefined}
      />,
    );
    await screen.findByText(/Current: system default/);
    const tokens = screen.getByLabelText(/Output tokens per task/i);
    await user.clear(tokens);
    await user.type(tokens, "0");
    const save = screen.getByRole("button", { name: "Save policy" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    expect(setAppPolicy).not.toHaveBeenCalled();
  });

  it("reloads fresh facts on a stale-revision conflict", async () => {
    const user = userEvent.setup();
    const getAppPolicy = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.GetAppPolicyResponse",
        policy: policy(),
      } satisfies GetAppPolicyResponse),
    );
    const setAppPolicy = vi.fn(() => Promise.reject(new ConnectError("stale", Code.Aborted)));
    render(
      <PolicyDialog
        installation={installation}
        workosClients={policyFixture(getAppPolicy, setAppPolicy)}
        onClose={() => undefined}
      />,
    );
    await screen.findByText(/Current: system default/);
    await user.click(screen.getByRole("radio", { name: /Block/ }));
    await user.click(screen.getByRole("button", { name: "Save policy" }));
    expect(await screen.findByText(/changed elsewhere/)).toBeTruthy();
    expect(getAppPolicy).toHaveBeenCalledTimes(2);
  });
});

describe("UsageView", () => {
  it("shows reserved and reported usage separately with honest cost", async () => {
    const listInstalledApps = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.app.v1.ListInstalledAppsResponse",
        installations: [installation],
        page: { $typeName: "workos.common.v1.PageResponse", nextPageToken: "" },
      }),
    );
    const getAppUsage = vi.fn(() =>
      Promise.resolve({
        $typeName: "workos.agent.v1.GetAppUsageResponse",
        usage: {
          $typeName: "workos.agent.v1.AgentAppDailyUsage",
          installationId: installation.id,
          utcDate: "2026-08-29",
          tasksReserved: 2n,
          outputTokensReserved: 512n,
          tasksRecorded: 2n,
          inputTokensRecorded: 19n,
          outputTokensRecorded: 8n,
          costDecimalRecorded: "",
          quotaBreached: false,
          policyRevision: 1n,
        } as GetAppUsageResponse["usage"],
      } satisfies GetAppUsageResponse),
    );
    render(
      <UsageView
        projectId="018f0000-0000-7000-8000-0000000000cc"
        workosClients={
          {
            appInstallations: { listInstalledApps },
            appUsage: { getAppUsage },
          } as unknown as WorkOSClients
        }
      />,
    );
    expect(await screen.findByText("fixture.app")).toBeTruthy();
    expect(screen.getByText("2 tasks · 512 output tokens")).toBeTruthy();
    expect(screen.getByText("2 tasks · 19 in / 8 out")).toBeTruthy();
    expect(screen.getByText("unavailable")).toBeTruthy();
    expect(screen.getByText("normal")).toBeTruthy();
  });
});
