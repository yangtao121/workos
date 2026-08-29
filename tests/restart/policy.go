package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	"github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	"github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
)

// policySeed drives the pre-run approval chain up to the pending state before
// the restart: install a bridge app, set a require-approval policy, open a
// surface, and submit one bridge run that must come back waiting. The durable
// facts are printed for policy-verify.
func policySeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)
	policies := agentv1connect.NewAgentAppPolicyServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-policy-%d", stamp)

	created, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: fmt.Sprintf("restart-policy-artifact-%d", stamp),
		Artifact:       &artifactv1.Artifact{Title: "Restart Policy"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("<!doctype html><title>Restart Policy</title><div id=\"root\"></div>")},
			},
		},
	}))
	if err != nil {
		return fmt.Errorf("create policy artifact: %w", err)
	}
	artifact := created.Msg.GetArtifact()
	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Restart Policy App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: %s
  artifactDigest: %s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [agent.task.run, agent.event.watch]
resources: {}
health: {}
maintainer: {}
`, appID, artifact.GetId(), artifact.GetDigest())
	if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-policy-register-%d", stamp),
		ManifestYaml:   []byte(manifest),
	})); err != nil {
		return fmt.Errorf("register policy app: %w", err)
	}
	project, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-policy-project-%d", stamp),
		Name:           "Restart Policy Space",
	}))
	if err != nil {
		return fmt.Errorf("create policy project: %w", err)
	}
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("restart-policy-install-%d", stamp),
		ProjectId:               project.Msg.GetProject().GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.Msg.GetProject().GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		return fmt.Errorf("install policy app: %w", err)
	}
	installationID := installed.Msg.GetInstallation().GetId()
	projectID := project.Msg.GetProject().GetId()

	surfaceKey := fmt.Sprintf("restart-policy-surface-%d", stamp)
	surface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    surfaceKey,
		AppInstanceId:     installationID,
		ProjectId:         projectID,
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1024, Height: 768, PixelRatio: 1},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		return fmt.Errorf("create policy surface: %w", err)
	}
	token := surface.Msg.GetSession().GetBridgeToken()

	setKey := fmt.Sprintf("restart-policy-set-%d", stamp)
	policySet, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: setKey,
		ProjectId:      projectID,
		InstallationId: installationID,
		Spec: &agentv1.AppAgentPolicySpec{
			ExecutionMode:                    agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL,
			MaxOutputTokensPerTask:           256,
			MaxRuntimeSecondsPerTask:         60,
			MaxTasksPerUtcDay:                5,
			MaxReservedOutputTokensPerUtcDay: 1280,
		},
		ExpectedPolicyRevision: 0,
	}))
	if err != nil {
		return fmt.Errorf("set policy: %w", err)
	}
	if policySet.Msg.GetPolicy().GetPolicyRevision() != 1 {
		return fmt.Errorf("policy revision must start at 1: %d", policySet.Msg.GetPolicy().GetPolicyRevision())
	}

	runKey := fmt.Sprintf("restart-policy-run-%d", stamp)
	goal := fmt.Sprintf("restart-approval-goal-%d", stamp)
	runRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey, Goal: goal,
	})
	runRequest.Header().Set("X-WorkOS-Bridge-Token", token)
	run, err := bridge.RunAgentTask(ctx, runRequest)
	if err != nil {
		return fmt.Errorf("bridge run: %w", err)
	}
	if run.Msg.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_WAITING {
		return fmt.Errorf("run must wait for approval: %+v", run.Msg)
	}

	fmt.Printf("%s %s %s %s %s %s %s %s", token, projectID, installationID, surfaceKey, setKey, runKey, run.Msg.GetTaskId(), goal)
	return nil
}

// policyVerify proves, after the restart: the explicit policy replays with
// revision 1, the pending approval and its waiting task survived, no quota
// was consumed while waiting, the owner can approve across the restart, and
// the task then executes to terminal with the usage bucket recording both the
// reservation and the observed usage.
func policyVerify(ctx context.Context, client *http.Client, baseURL, token, projectID, installationID, surfaceKey, setKey, runKey, taskID, goal string) error {
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)
	policies := agentv1connect.NewAgentAppPolicyServiceClient(client, baseURL)
	approvals := agentv1connect.NewAgentApprovalServiceClient(client, baseURL)
	usages := agentv1connect.NewAgentAppUsageServiceClient(client, baseURL)

	replayedPolicy, err := policies.SetAppPolicy(ctx, connect.NewRequest(&agentv1.SetAppPolicyRequest{
		IdempotencyKey: setKey,
		ProjectId:      projectID,
		InstallationId: installationID,
		Spec: &agentv1.AppAgentPolicySpec{
			ExecutionMode:                    agentv1.AppAgentExecutionMode_APP_AGENT_EXECUTION_MODE_REQUIRE_APPROVAL,
			MaxOutputTokensPerTask:           256,
			MaxRuntimeSecondsPerTask:         60,
			MaxTasksPerUtcDay:                5,
			MaxReservedOutputTokensPerUtcDay: 1280,
		},
		ExpectedPolicyRevision: 0,
	}))
	if err != nil || replayedPolicy.Msg.GetPolicy().GetPolicyRevision() != 1 {
		return fmt.Errorf("policy replay must return revision 1: %v %+v", err, replayedPolicy.Msg.GetPolicy())
	}

	listed, err := approvals.ListApprovals(ctx, connect.NewRequest(&agentv1.ListApprovalsRequest{
		ProjectId: projectID, State: agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_PENDING,
	}))
	if err != nil {
		return fmt.Errorf("list approvals: %w", err)
	}
	var approval *agentv1.AgentApproval
	for _, candidate := range listed.Msg.GetApprovals() {
		if candidate.GetTaskId() == taskID {
			approval = candidate
		}
	}
	if approval == nil {
		return errors.New("pending approval did not survive the restart")
	}
	usageBefore, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: projectID, InstallationId: installationID,
	}))
	if err != nil {
		return fmt.Errorf("read usage: %w", err)
	}
	if usageBefore.Msg.GetUsage().GetTasksReserved() != 0 {
		return fmt.Errorf("waiting task must not reserve quota: %+v", usageBefore.Msg.GetUsage())
	}

	// The consumed run key replays the same waiting task across the restart.
	runRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{IdempotencyKey: runKey, Goal: goal})
	runRequest.Header().Set("X-WorkOS-Bridge-Token", token)
	replayed, err := bridge.RunAgentTask(ctx, runRequest)
	if err != nil {
		return fmt.Errorf("run replay failed: %w", err)
	}
	if replayed.Msg.GetTaskId() != taskID {
		return fmt.Errorf("run replay diverged: %s vs %s", replayed.Msg.GetTaskId(), taskID)
	}

	decided, err := approvals.DecideApproval(ctx, connect.NewRequest(&agentv1.DecideApprovalRequest{
		IdempotencyKey: "restart-policy-decide-" + taskID,
		ApprovalId:     approval.GetApprovalId(),
		Decision:       agentv1.AppAgentApprovalDecision_APP_AGENT_APPROVAL_DECISION_APPROVE,
	}))
	if err != nil {
		return fmt.Errorf("approve across restart: %w", err)
	}
	if decided.Msg.GetApproval().GetState() != agentv1.AppAgentApprovalState_APP_AGENT_APPROVAL_STATE_APPROVED {
		return fmt.Errorf("approve across restart left state %v", decided.Msg.GetApproval().GetState())
	}

	// Watch to terminal through the same bridge token.
	after := int64(0)
	terminal := false
	for round := 0; round < 100 && !terminal; round++ {
		watchRequest := connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{TaskId: taskID, AfterSequence: after})
		watchRequest.Header().Set("X-WorkOS-Bridge-Token", token)
		stream, err := bridge.WatchAgentTaskEvents(ctx, watchRequest)
		if err != nil {
			return fmt.Errorf("watch: %w", err)
		}
		for stream.Receive() {
			event := stream.Msg().GetEvent()
			switch event.Event.(type) {
			case *agentv1.AgentEvent_RunCompleted, *agentv1.AgentEvent_RunFailed, *agentv1.AgentEvent_RunCancelled:
				terminal = true
			}
			after = event.GetSequence()
		}
		stream.Close()
	}
	if !terminal {
		return errors.New("approved task never reached a terminal event")
	}

	usageAfter, err := usages.GetAppUsage(ctx, connect.NewRequest(&agentv1.GetAppUsageRequest{
		ProjectId: projectID, InstallationId: installationID,
	}))
	if err != nil {
		return fmt.Errorf("read final usage: %w", err)
	}
	bucket := usageAfter.Msg.GetUsage()
	if bucket.GetTasksReserved() != 1 || bucket.GetOutputTokensReserved() != 256 {
		return fmt.Errorf("reservation missing after restart+approve: %+v", bucket)
	}
	if bucket.GetTasksRecorded() != 1 || bucket.GetOutputTokensRecorded() <= 0 {
		return fmt.Errorf("recorded usage missing: %+v", bucket)
	}
	return nil
}
