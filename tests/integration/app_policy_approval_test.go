//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	bridgev1connect "github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
)

func policyClients(t *testing.T) agentv1connect.AgentAppPolicyServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	return agentv1connect.NewAgentAppPolicyServiceClient(httpClient, gatewayBaseURL())
}

func approvalClients(t *testing.T) agentv1connect.AgentApprovalServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	return agentv1connect.NewAgentApprovalServiceClient(httpClient, gatewayBaseURL())
}

func usageClients(t *testing.T) agentv1connect.AgentAppUsageServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	return agentv1connect.NewAgentAppUsageServiceClient(httpClient, gatewayBaseURL())
}

// policyFixture registers one bridge-capable app, installs it with both
// bridge grants into a fresh project, and opens one surface.
type policyFixture struct {
	appID        string
	projectID    string
	installation string
	bridgeToken  string
	stamp        int64
}

func newPolicyFixture(t *testing.T, ctx context.Context, name string) *policyFixture {
	t.Helper()
	stamp := time.Now().UnixNano()
	artifacts := artifactClients(t)
	surfaces := surfaceClients(t)
	projects := integrationProjectClients(t)
	installations := installationClients(t)
	registry := appRegistryClients(t)

	appID := fmt.Sprintf("policy-agent-%d", stamp)
	artifact := createArtifact(t, ctx, artifacts, fmt.Sprintf("policy-create-%d", stamp), name+" App", bundleFiles(), "index.html")
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("policy-register-%d", stamp),
		ManifestYaml:   bridgeAgentManifest(appID, name+" Agent", artifact.GetId(), artifact.GetDigest()),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}
	project := createIntegrationProject(t, ctx, projects, name, fmt.Sprintf("policy-project-%d", stamp))
	installResponse, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("policy-install-%d", stamp),
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	installation := installResponse.Msg.GetInstallation()
	surface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    fmt.Sprintf("policy-surface-%d", stamp),
		AppInstanceId:     installation.GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1024, Height: 768, PixelRatio: 1},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		t.Fatalf("create surface: %v", err)
	}
	return &policyFixture{
		appID:        appID,
		projectID:    project.GetId(),
		installation: installation.GetId(),
		bridgeToken:  surface.Msg.GetSession().GetBridgeToken(),
		stamp:        stamp,
	}
}

func (f *policyFixture) run(t *testing.T, ctx context.Context, bridge bridgev1connect.AppBridgeServiceClient, key, goal string) *bridgev1.RunAgentTaskResponse {
	t.Helper()
	run, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: key, Goal: goal,
	}, f.bridgeToken))
	if err != nil {
		t.Fatalf("bridge run: %v", err)
	}
	return run.Msg
}

func requireApprovalPolicy(mode agentv1.AppAgentExecutionMode, tokens, runtime, tasks, reserved int64) *agentv1.AppAgentPolicySpec {
	return &agentv1.AppAgentPolicySpec{
		ExecutionMode:                    mode,
		MaxOutputTokensPerTask:           tokens,
		MaxRuntimeSecondsPerTask:         runtime,
		MaxTasksPerUtcDay:                tasks,
		MaxReservedOutputTokensPerUtcDay: reserved,
	}
}

// TestAppAgentRequireApprovalVerticalSlice proves the whole pre-run approval
// chain through the real gateway, Core, runtime-host, and Fake Harness:
// explicit require-approval policy → waiting run without quota consumption →
// owner approval → queued task executes → usage projection records both the
// reservation and the observed usage.
func TestAppAgentRequireApprovalVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Approval Slice")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	approvals := approvalClients(t)
	usages := usageClients(t)
	agentTasks := agentTaskClients(t)

	// The system default is finite and visible before any explicit policy.
	defaultPolicy, err := policies.GetAppPolicy(ctx, connect.NewRequest(&agentv1.GetAppPolicyRequest{
		ProjectId: fixture.projectID, InstallationId: fixture.installation,
	}))
	if err != nil {
		t.Fatalf("read default policy: %v", err)
	}
	if defaultPolicy.Msg.GetPolicy().GetSource() != agentv1.AppAgentPolicySource_APP_AGENT_POLICY_SOURCE_SYSTEM_DEFAULT ||
		defaultPolicy.Msg.GetPolicy().GetSpec().GetMaxOutputTokensPerTask() <= 0 ||
		defaultPolicy.Msg.GetPolicy().GetSpec().GetMaxTasksPerUtcDay() <= 0 {
		t.Fatalf("system default must be finite and labeled: %+v", defaultPolicy.Msg.GetPolicy())
	}

	// Set the explicit require-approval policy.
	setKey := fmt.Sprintf("policy-set-%d", fixture.stamp)
	spec := requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL, 256, 60, 5, 1280)
	set, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: setKey, ProjectId: fixture.projectID, InstallationId: fixture.installation,
		Spec: spec, ExpectedPolicyRevision: 0,
	}))
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if set.Msg.GetPolicy().GetPolicyRevision() != 1 || set.Msg.GetPolicy().GetSource() != agentv1.AppAgentPolicySource_APP_AGENT_POLICY_SOURCE_EXPLICIT {
		t.Fatalf("unexpected first policy response: %+v", set.Msg.GetPolicy())
	}

	t.Run("PolicyIdempotency", func(t *testing.T) {
		// Same key, same request replays the exact first response.
		replayed, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
			IdempotencyKey: setKey, ProjectId: fixture.projectID, InstallationId: fixture.installation,
			Spec: spec, ExpectedPolicyRevision: 0,
		}))
		if err != nil || replayed.Msg.GetPolicy().GetPolicyRevision() != 1 {
			t.Fatalf("policy replay diverged: %v %+v", err, replayed.Msg.GetPolicy())
		}
		// Same key, different request is a stable Aborted.
		_, conflict := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
			IdempotencyKey: setKey, ProjectId: fixture.projectID, InstallationId: fixture.installation,
			Spec: requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW, 256, 60, 5, 1280),
		}))
		if connect.CodeOf(conflict) != connect.CodeAborted {
			t.Fatalf("same-key different-spec verdict: %v", conflict)
		}
		// Stale expected revision aborts.
		_, stale := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
			IdempotencyKey: setKey + "-stale", ProjectId: fixture.projectID, InstallationId: fixture.installation,
			Spec: spec, ExpectedPolicyRevision: 99,
		}))
		if connect.CodeOf(stale) != connect.CodeAborted {
			t.Fatalf("stale revision verdict: %v", stale)
		}
		// Invalid limits are InvalidArgument without consuming anything.
		_, invalid := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
			IdempotencyKey: setKey + "-invalid", ProjectId: fixture.projectID, InstallationId: fixture.installation,
			Spec: requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW, 0, 60, 5, 1280),
		}))
		if connect.CodeOf(invalid) != connect.CodeInvalidArgument {
			t.Fatalf("zero-limit verdict: %v", invalid)
		}
	})

	// The bridge run waits; it does not queue, execute, or reserve quota.
	goal := fmt.Sprintf("Approval fixture %d", fixture.stamp)
	runKey := fmt.Sprintf("approval-run-%d", fixture.stamp)
	run := fixture.run(t, ctx, bridge, runKey, goal)
	if run.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_WAITING {
		t.Fatalf("require-approval run must wait: %+v", run)
	}

	usage, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: fixture.projectID, InstallationId: fixture.installation,
	}))
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.Msg.GetUsage().GetTasksReserved() != 0 || usage.Msg.GetUsage().GetOutputTokensReserved() != 0 {
		t.Fatalf("waiting run consumed quota: %+v", usage.Msg.GetUsage())
	}

	// Exactly one pending approval is visible to the owner, with a bounded
	// untrusted excerpt.
	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: fixture.projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil || len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("pending approvals wrong: %v %+v", err, listed.Msg.GetApprovals())
	}
	approval := listed.Msg.GetApprovals()[0]
	if approval.GetTaskId() != run.GetTaskId() || approval.GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING ||
		approval.GetMaxOutputTokensPerTask() != 256 || approval.GetPolicyRevision() != 1 || approval.GetProviderId() != "fake" {
		t.Fatalf("approval snapshot wrong: %+v", approval)
	}
	if approval.GetGoalExcerpt() == "" || len(approval.GetGoalExcerpt()) > 512 {
		t.Fatalf("excerpt bounds violated: %q", approval.GetGoalExcerpt())
	}
	// Foreign/unknown approval IDs are indistinguishable NotFound.
	if _, err := approvals.GetApproval(ctx, connect.NewRequest(&agentv1.GetApprovalRequest{
		ApprovalId: "018f0000-0000-7000-8000-0000000000ff",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown approval verdict: %v", err)
	}

	// Approve: the waiting task queues, executes on the Fake Harness, and
	// reaches a terminal state through the owner's watch stream.
	decided, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("approval-decide-%d", fixture.stamp),
		ApprovalId:     approval.GetApprovalId(),
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
	}))
	if err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	if decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED {
		t.Fatalf("approve state wrong: %+v", decided.Msg.GetApproval())
	}

	t.Run("DecisionIdempotency", func(t *testing.T) {
		// Same key, same decision replays the approved fact.
		replayed, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
			IdempotencyKey: fmt.Sprintf("approval-decide-%d", fixture.stamp),
			ApprovalId:     approval.GetApprovalId(),
			Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
		}))
		if err != nil || replayed.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED {
			t.Fatalf("decision replay diverged: %v %+v", err, replayed.Msg.GetApproval())
		}
		// A different key cannot re-decide.
		_, second := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
			IdempotencyKey: fmt.Sprintf("approval-decide-2-%d", fixture.stamp),
			ApprovalId:     approval.GetApprovalId(),
			Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT,
		}))
		if connect.CodeOf(second) != connect.CodeAborted {
			t.Fatalf("re-decide verdict: %v", second)
		}
	})

	// Watch the approved task to terminal; the approval_required and
	// approval_decided events are on the durable stream the app observes.
	after := int64(0)
	sawApprovalRequired, sawApproved, sawTerminal := false, false, false
	for round := 0; round < 200; round++ {
		stream, err := bridge.WatchAgentTaskEvents(ctx, watchRequest(run.GetTaskId(), after, fixture.bridgeToken))
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
		for stream.Receive() {
			event := stream.Msg().GetEvent()
			switch {
			case event.GetApprovalRequired() != nil:
				sawApprovalRequired = true
				if event.GetApprovalRequired().GetApprovalId() != approval.GetApprovalId() {
					t.Fatalf("approval event mismatch: %+v", event)
				}
			case event.GetApprovalDecided() != nil:
				sawApproved = true
			case event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil:
				sawTerminal = true
			}
			after = event.GetSequence()
		}
		stream.Close()
		if sawTerminal {
			break
		}
	}
	if !sawApprovalRequired || !sawApproved || !sawTerminal {
		t.Fatalf("event chain incomplete: required=%v decided=%v terminal=%v", sawApprovalRequired, sawApproved, sawTerminal)
	}
	task, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: run.GetTaskId()}))
	if err != nil || task.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("approved task must complete: %v %+v", err, task.Msg.GetTask())
	}
	// The server-derived budget was injected into the canonical input.
	if budget := task.Msg.GetTask().GetInput().GetBudget(); budget.GetMaxTokens() != 256 || budget.GetMaxRuntimeSeconds() != 60 || budget.GetMaxCostDecimal() != "" {
		t.Fatalf("budget snapshot wrong: %+v", budget)
	}

	// Usage: the reservation happened at approval; the fake harness reported
	// the observed usage against the cap.
	finalUsage, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: fixture.projectID, InstallationId: fixture.installation,
	}))
	if err != nil {
		t.Fatalf("read final usage: %v", err)
	}
	bucket := finalUsage.Msg.GetUsage()
	if bucket.GetTasksReserved() != 1 || bucket.GetOutputTokensReserved() != 256 {
		t.Fatalf("reservation missing: %+v", bucket)
	}
	if bucket.GetTasksRecorded() != 1 || bucket.GetOutputTokensRecorded() <= 0 || bucket.GetOutputTokensRecorded() > 256 {
		t.Fatalf("recorded usage wrong: %+v", bucket)
	}
	if bucket.GetQuotaBreached() {
		t.Fatal("healthy run must not breach the bucket")
	}
	if bucket.GetCostDecimalRecorded() != "" {
		t.Fatalf("unavailable cost must be absent, not zero: %q", bucket.GetCostDecimalRecorded())
	}
}

// TestAppAgentRejectNeverExecutes proves the reject path: terminal task,
// durable decision event, zero reservations, and a watch stream the app can
// read without re-handshaking.
func TestAppAgentRejectNeverExecutes(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Reject Slice")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	approvals := approvalClients(t)
	usages := usageClients(t)

	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("reject-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 0,
	})); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	run := fixture.run(t, ctx, bridge, fmt.Sprintf("reject-run-%d", fixture.stamp), fmt.Sprintf("Reject fixture %d", fixture.stamp))
	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: fixture.projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil || len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("pending approvals wrong: %v", err)
	}
	decided, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("reject-decide-%d", fixture.stamp),
		ApprovalId:     listed.Msg.GetApprovals()[0].GetApprovalId(),
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT,
	}))
	if err != nil || decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED {
		t.Fatalf("reject failed: %v %+v", err, decided.Msg.GetApproval())
	}

	// The task is terminal-cancelled with the decision event on the stream;
	// the same bridge token (no re-handshake) can watch it.
	after := int64(0)
	sawApproved, sawTerminal := false, false
	for round := 0; round < 50 && !sawTerminal; round++ {
		stream, err := bridge.WatchAgentTaskEvents(ctx, watchRequest(run.GetTaskId(), after, fixture.bridgeToken))
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
		for stream.Receive() {
			event := stream.Msg().GetEvent()
			if event.GetApprovalDecided() != nil && event.GetApprovalDecided().GetDecision() == agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT {
				sawApproved = true
			}
			if event.GetRunCancelled() != nil {
				sawTerminal = true
			}
			after = event.GetSequence()
		}
		stream.Close()
	}
	if !sawApproved || !sawTerminal {
		t.Fatalf("reject chain incomplete: decided=%v terminal=%v", sawApproved, sawTerminal)
	}
	usage, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: fixture.projectID, InstallationId: fixture.installation,
	}))
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.Msg.GetUsage().GetTasksReserved() != 0 || usage.Msg.GetUsage().GetTasksRecorded() != 0 {
		t.Fatalf("rejected task consumed quota: %+v", usage.Msg.GetUsage())
	}
}

// TestAppAgentBlockAndQuotaFailClosed proves block mode, the quota boundary,
// the breach circuit, and that failed adjudications never consume the run key.
func TestAppAgentBlockAndQuotaFailClosed(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Quota Slice")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	usages := usageClients(t)

	// Block mode: sanitized PermissionDenied, nothing created.
	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("block-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_BLOCK, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 0,
	})); err != nil {
		t.Fatalf("set block policy: %v", err)
	}
	blockKey := fmt.Sprintf("block-run-%d", fixture.stamp)
	if _, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: blockKey, Goal: "blocked goal",
	}, fixture.bridgeToken)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("block verdict: %v", err)
	}
	// The run key was not consumed: switching to a one-task allowance makes
	// the same key a normal first run.
	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("quota-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW, 64, 60, 1, 64),
		ExpectedPolicyRevision: 1,
	})); err != nil {
		t.Fatalf("set quota policy: %v", err)
	}
	first := fixture.run(t, ctx, bridge, blockKey, "quota fixture goal")
	if first.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED {
		t.Fatalf("first allowed run must queue: %+v", first)
	}

	// The bucket is full: the next fresh run is ResourceExhausted.
	secondKey := fmt.Sprintf("quota-second-%d", fixture.stamp)
	if _, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: secondKey, Goal: "second goal",
	}, fixture.bridgeToken)); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("exhausted verdict: %v", err)
	}
	// The failed run's key is still unconsumed: after raising the daily
	// allowance (a real policy change, same bucket), the same key succeeds.
	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("quota-raise-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW, 64, 60, 2, 128),
		ExpectedPolicyRevision: 2,
	})); err != nil {
		t.Fatalf("raise policy: %v", err)
	}
	second := fixture.run(t, ctx, bridge, secondKey, "second goal")
	if second.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED {
		t.Fatalf("second allowed run must queue: %+v", second)
	}
	usage, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: fixture.projectID, InstallationId: fixture.installation,
	}))
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.Msg.GetUsage().GetTasksReserved() != 2 || usage.Msg.GetUsage().GetOutputTokensReserved() != 128 {
		t.Fatalf("bucket must keep its consumption across the policy change: %+v", usage.Msg.GetUsage())
	}
}

// TestAppAgentPolicyChangeInvalidatesPendingApprovals proves that a real
// policy change atomically expires pending approvals, terminates their
// waiting tasks, and that deciding an expired approval fails closed without
// ever queueing it.
func TestAppAgentPolicyChangeInvalidatesPendingApprovals(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Invalidate Slice")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	approvals := approvalClients(t)
	agentTasks := agentTaskClients(t)

	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("invalidate-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 0,
	})); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	run := fixture.run(t, ctx, bridge, fmt.Sprintf("invalidate-run-%d", fixture.stamp), fmt.Sprintf("Invalidation fixture %d", fixture.stamp))
	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: fixture.projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil || len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("pending approvals wrong: %v", err)
	}
	approvalID := listed.Msg.GetApprovals()[0].GetApprovalId()

	// Real policy change: require_approval → allow.
	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("invalidate-change-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_ALLOW, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 1,
	})); err != nil {
		t.Fatalf("change policy: %v", err)
	}

	if _, err := approvals.GetApproval(ctx, connect.NewRequest(&agentv1.GetApprovalRequest{ApprovalId: approvalID})); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	decided, getErr := approvals.GetApproval(ctx, connect.NewRequest(&agentv1.GetApprovalRequest{ApprovalId: approvalID}))
	if getErr != nil || decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_EXPIRED {
		t.Fatalf("approval must be expired: %v %+v", getErr, decided.Msg.GetApproval())
	}
	if _, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("invalidate-decide-%d", fixture.stamp),
		ApprovalId:     approvalID,
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("deciding an expired approval verdict: %v", err)
	}
	task, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: run.GetTaskId()}))
	if err != nil || task.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
		t.Fatalf("invalidated task must be cancelled: %v %+v", err, task.Msg.GetTask())
	}
	// A fresh run under the new policy works immediately.
	fresh := fixture.run(t, ctx, bridge, fmt.Sprintf("invalidate-fresh-%d", fixture.stamp), "fresh after change")
	if fresh.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED {
		t.Fatalf("fresh run must follow the new policy: %+v", fresh)
	}
}

// TestAppAgentApprovalDecisionConcurrency races opposite owner decisions on
// one pending approval: exactly one wins, the loser gets a stable verdict,
// and the task ends in exactly the winner's state.
func TestAppAgentApprovalDecisionConcurrency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Decision Race")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	approvals := approvalClients(t)

	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("race-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 0,
	})); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	fixture.run(t, ctx, bridge, fmt.Sprintf("race-run-%d", fixture.stamp), fmt.Sprintf("Race fixture %d", fixture.stamp))
	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: fixture.projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil || len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("pending approvals wrong: %v", err)
	}
	approvalID := listed.Msg.GetApprovals()[0].GetApprovalId()

	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
			IdempotencyKey: fmt.Sprintf("race-approve-%d", fixture.stamp), ApprovalId: approvalID,
			Decision: agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
		}))
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
			IdempotencyKey: fmt.Sprintf("race-reject-%d", fixture.stamp), ApprovalId: approvalID,
			Decision: agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT,
		}))
		results <- err
	}()
	wg.Wait()
	close(results)

	winners, losers := 0, 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if connect.CodeOf(err) == connect.CodeAborted || connect.CodeOf(err) == connect.CodeFailedPrecondition {
			losers++
			continue
		}
		t.Fatalf("unexpected decision verdict: %v", err)
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("exactly one winner expected: winners=%d losers=%d", winners, losers)
	}
	final, err := approvals.GetApproval(ctx, connect.NewRequest(&agentv1.GetApprovalRequest{ApprovalId: approvalID}))
	if err != nil {
		t.Fatal(err)
	}
	state := final.Msg.GetApproval().GetState()
	if state != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED && state != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED {
		t.Fatalf("terminal approval state expected: %v", state)
	}
}

// TestAppAgentForeignIsolation proves owner isolation of the new services:
// another identity cannot see or decide this owner's facts, and malformed
// input is rejected before any fact is touched.
func TestAppAgentForeignIsolation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Isolation Slice")
	policies := policyClients(t)
	approvals := approvalClients(t)
	usages := usageClients(t)

	// The gateway injects the configured owner identity; malformed UUIDs are
	// InvalidArgument before any existence leak.
	if _, err := policies.GetAppPolicy(ctx, connect.NewRequest(&agentv1.GetAppPolicyRequest{
		ProjectId: "not-a-uuid", InstallationId: fixture.installation,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed project verdict: %v", err)
	}
	if _, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: "not-a-uuid",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed list verdict: %v", err)
	}
	if _, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: fixture.projectID, InstallationId: "018f0000-0000-7000-8000-0000000000de",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign installation must be NotFound: %v", err)
	}
}

// TestAppAgentRejectSurvivesUninstalledWorld proves the decision asymmetry:
// once the installation is gone an approve must still fail closed — the world
// the approval was created in no longer exists — while a reject must keep
// working, so the owner can never be trapped with an undecideable pending
// approval. Before the uninstall, an approve against the uninstalled world is
// refused; after it, the reject terminates the waiting task normally.
func TestAppAgentRejectSurvivesUninstalledWorld(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newPolicyFixture(t, ctx, "Uninstalled Reject Slice")
	bridge := bridgeClients(t)
	policies := policyClients(t)
	approvals := approvalClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	if _, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: fmt.Sprintf("uninstalled-policy-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		Spec:                   requireApprovalPolicy(agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL, 256, 60, 5, 1280),
		ExpectedPolicyRevision: 0,
	})); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	run := fixture.run(t, ctx, bridge, fmt.Sprintf("uninstalled-run-%d", fixture.stamp), fmt.Sprintf("Uninstalled reject fixture %d", fixture.stamp))
	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: fixture.projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil || len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("pending approvals wrong: %v", err)
	}
	approvalID := listed.Msg.GetApprovals()[0].GetApprovalId()

	project, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: fixture.projectID}))
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
		IdempotencyKey: fmt.Sprintf("uninstalled-uninstall-%d", fixture.stamp),
		ProjectId:      fixture.projectID, InstallationId: fixture.installation,
		ExpectedProjectRevision: project.Msg.GetProject().GetRevision(),
	})); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Approve fails closed: no installation, no enqueue, approval stays
	// pending.
	if _, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("uninstalled-approve-%d", fixture.stamp),
		ApprovalId:     approvalID,
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("approve after uninstall verdict: %v", err)
	}
	still, err := approvals.GetApproval(ctx, connect.NewRequest(&agentv1.GetApprovalRequest{ApprovalId: approvalID}))
	if err != nil || still.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING {
		t.Fatalf("approval must stay pending after refused approve: %v %+v", err, still.Msg.GetApproval())
	}

	// Reject works: the waiting task terminates without ever queueing.
	decided, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("uninstalled-reject-%d", fixture.stamp),
		ApprovalId:     approvalID,
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT,
	}))
	if err != nil || decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED {
		t.Fatalf("reject after uninstall failed: %v %+v", err, decided.Msg.GetApproval())
	}
	task, err := agentTaskClients(t).GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: run.GetTaskId()}))
	if err != nil || task.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
		t.Fatalf("rejected task must be cancelled: %v %+v", err, task.Msg.GetTask())
	}
	// The refused approve key never decided anything; the reject key replays
	// exactly.
	if _, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: fmt.Sprintf("uninstalled-reject-%d", fixture.stamp),
		ApprovalId:     approvalID,
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_REJECT,
	})); err != nil || decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_REJECTED {
		t.Fatalf("reject replay diverged: %v", err)
	}
}
