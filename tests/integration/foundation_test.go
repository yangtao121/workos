//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

func TestProjectToHarnessVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	projects := projectv1connect.NewProjectServiceClient(httpClient, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(httpClient, baseURL)
	catalogs := harnessv1connect.NewHarnessCatalogServiceClient(httpClient, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(httpClient, baseURL)

	catalog, err := catalogs.GetHarnessCatalog(ctx, connect.NewRequest(&harnessv1.GetHarnessCatalogRequest{}))
	if err != nil {
		t.Fatalf("get public provider catalog: %v", err)
	}
	if catalog.Msg.GetDefaultProviderId() != "fake" {
		t.Fatalf("unexpected default provider: %q", catalog.Msg.GetDefaultProviderId())
	}
	providers := make(map[string]*harnessv1.HarnessProviderInfo, len(catalog.Msg.GetProviders()))
	for _, provider := range catalog.Msg.GetProviders() {
		providers[provider.GetId()] = provider
	}
	if fake := providers["fake"]; fake == nil || fake.GetHealth() != commonv1.HealthState_HEALTH_STATE_HEALTHY || !fake.GetCapabilities().GetStreaming() || !fake.GetCapabilities().GetUsageReporting() {
		t.Fatalf("fake provider is not truthfully available: %#v", fake)
	}
	if deepSeek := providers["deepseek"]; deepSeek == nil || deepSeek.GetHealth() != commonv1.HealthState_HEALTH_STATE_UNAVAILABLE || deepSeek.GetUnavailableReason() == "" {
		t.Fatalf("disabled DeepSeek provider is not safely reported: %#v", deepSeek)
	}

	key := fmt.Sprintf("integration-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Foundation Integration", Icon: "◈",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	if project.GetId() == "" || project.GetRevision() != 1 {
		t.Fatalf("unexpected project: %#v", project)
	}

	// Same key + same request replays the exact first response: same id,
	// same revision, same timestamps — even though the wire request is a
	// separate call.
	repeated, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Foundation Integration", Icon: "◈",
	}))
	if err != nil {
		t.Fatalf("repeat idempotent create: %v", err)
	}
	replay := repeated.Msg.GetProject()
	if replay.GetId() != project.GetId() || replay.GetRevision() != project.GetRevision() ||
		!replay.GetCreatedAt().AsTime().Equal(project.GetCreatedAt().AsTime()) ||
		!replay.GetUpdatedAt().AsTime().Equal(project.GetUpdatedAt().AsTime()) {
		t.Fatalf("idempotent replay must return the exact first response: %#v vs %#v", replay, project)
	}

	// Same key + different request is a stable, sanitized conflict — it
	// must never silently return the old project.
	_, conflictErr := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "ignored duplicate name",
	}))
	if connect.CodeOf(conflictErr) != connect.CodeAborted {
		t.Fatalf("same key with a different request must be Aborted, got %v", conflictErr)
	}

	// A later Update must not leak into the replay: the first response's
	// facts are pinned durably.
	updatedName := "Foundation Verified"
	updated, err := projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(), Name: &updatedName,
	}))
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if updated.Msg.GetProject().GetRevision() != 2 {
		t.Fatalf("expected revision 2, got %d", updated.Msg.GetProject().GetRevision())
	}
	afterUpdate, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Foundation Integration", Icon: "◈",
	}))
	if err != nil {
		t.Fatalf("replay after update: %v", err)
	}
	postUpdateReplay := afterUpdate.Msg.GetProject()
	if postUpdateReplay.GetName() != "Foundation Integration" || postUpdateReplay.GetRevision() != 1 {
		t.Fatalf("replay after update must return the first response snapshot: %#v", postUpdateReplay)
	}
	_, err = projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: 1, Name: &updatedName,
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: updated.Msg.GetProject().GetId(), ExpectedRevision: updated.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	}))
	if err != nil {
		t.Fatalf("bind fake through public orchestration: %v", err)
	}
	boundProject := bound.Msg.GetProject()
	if binding := boundProject.GetHarnessBinding(); boundProject.GetRevision() != 3 || binding.GetProviderId() != "fake" || binding.GetInstancePolicy() != projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL || binding.GetResourcePolicyId() != "project-no-tools" || binding.GetCredentialRef() != "" {
		t.Fatalf("server binding preset was not applied safely: %#v", boundProject)
	}
	cleared, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: boundProject.GetId(), ExpectedRevision: boundProject.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_UseGlobalDefault{UseGlobalDefault: true},
	}))
	if err != nil {
		t.Fatalf("clear binding through public orchestration: %v", err)
	}
	if cleared.Msg.GetProject().GetRevision() != 4 || cleared.Msg.GetProject().GetHarnessBinding() != nil {
		t.Fatalf("global-default selection did not clear binding: %#v", cleared.Msg.GetProject())
	}

	taskKey := fmt.Sprintf("integration-task-%d", time.Now().UnixNano())
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "verify durable fake harness events",
		},
	}))
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	if submitted.Msg.GetTask().GetProviderId() != "fake" {
		t.Fatalf("global-default task did not snapshot fake: %#v", submitted.Msg.GetTask())
	}
	taskID := submitted.Msg.GetTask().GetId()
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID}))
	if err != nil {
		t.Fatalf("watch task: %v", err)
	}
	var sequence int64
	startedProvider := ""
	terminalEvents := 0
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		if event.GetSequence() != sequence+1 {
			t.Fatalf("event sequence jumped from %d to %d", sequence, event.GetSequence())
		}
		sequence = event.GetSequence()
		if started := event.GetRunStarted(); started != nil {
			startedProvider = started.GetProviderId()
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminalEvents++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("task stream failed: %v", err)
	}
	if sequence < 3 || terminalEvents != 1 || startedProvider != "fake" {
		t.Fatalf("expected ordered fake stream with one terminal event, sequence=%d terminal=%d provider=%q", sequence, terminalEvents, startedProvider)
	}
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		t.Fatalf("get final task: %v", err)
	}
	if final.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("expected completed task, got %s", final.Msg.GetTask().GetState())
	}

	resumed, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{
		TaskId: taskID, AfterSequence: sequence - 1,
	}))
	if err != nil {
		t.Fatalf("resume task stream: %v", err)
	}
	resumedCount := 0
	for resumed.Receive() {
		resumedCount++
	}
	if err := resumed.Err(); err != nil {
		t.Fatalf("resumed stream failed: %v", err)
	}
	if resumedCount != 1 {
		t.Fatalf("expected one resumed event, got %d", resumedCount)
	}
}
