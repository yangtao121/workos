//go:build integration && deepseekfixture

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
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

func TestDeepSeekProjectBindingFixtureVerticalSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	waitForFixture(t, ctx, client)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	catalogs := harnessv1connect.NewHarnessCatalogServiceClient(client, baseURL)

	described, err := catalogs.GetHarnessCatalog(ctx, connect.NewRequest(&harnessv1.GetHarnessCatalogRequest{}))
	if err != nil {
		t.Fatalf("get public provider catalog: %v", err)
	}
	deepSeekAvailable := false
	for _, provider := range described.Msg.GetProviders() {
		if provider.GetId() == "deepseek" && provider.GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY && provider.GetCapabilities().GetStreaming() && provider.GetCapabilities().GetUsageReporting() {
			deepSeekAvailable = true
		}
	}
	if !deepSeekAvailable {
		t.Fatalf("DeepSeek fixture provider is not available: %#v", described.Msg.GetProviders())
	}

	key := fmt.Sprintf("deepseek-fixture-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "DeepSeek Fixture",
	}))
	if err != nil {
		t.Fatalf("create DeepSeek project: %v", err)
	}
	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "deepseek"},
	}))
	if err != nil {
		t.Fatalf("bind DeepSeek through public orchestration: %v", err)
	}
	project := bound.Msg.GetProject()
	// The server-derived credential_ref must be present (the operator stored
	// the synthetic fixture credential in the vault) and is a non-bearer
	// opaque UUID the client never chose (ADR-0009).
	if binding := project.GetHarnessBinding(); project.GetRevision() != 2 || binding.GetProviderId() != "deepseek" || len(binding.GetCredentialRef()) != 36 || binding.GetInstancePolicy() != projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL || binding.GetResourcePolicyId() != "project-no-tools" {
		t.Fatalf("unexpected server-owned DeepSeek binding preset: %#v", project)
	}
	taskKey := "task-" + key
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "prove the DeepSeek project binding fixture",
			Budget: &agentv1.AgentBudget{MaxTokens: 2048, MaxRuntimeSeconds: 20},
		},
	}))
	if err != nil {
		t.Fatalf("submit DeepSeek task: %v", err)
	}
	task := submitted.Msg.GetTask()
	if task.GetProviderId() != "deepseek" {
		t.Fatalf("task did not snapshot DeepSeek binding: %#v", task)
	}

	rebound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	}))
	if err != nil {
		t.Fatalf("change project binding after submit: %v", err)
	}
	if rebound.Msg.GetProject().GetRevision() != 3 || rebound.Msg.GetProject().GetHarnessBinding().GetProviderId() != "fake" {
		t.Fatalf("unexpected rebound Project: %#v", rebound.Msg.GetProject())
	}
	repeated, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "this changed retry payload must be ignored",
		},
	}))
	if err != nil {
		t.Fatalf("repeat idempotent task: %v", err)
	}
	if repeated.Msg.GetTask().GetId() != task.GetId() || repeated.Msg.GetTask().GetProviderId() != "deepseek" {
		t.Fatalf("idempotency did not preserve provider snapshot: %#v", repeated.Msg.GetTask())
	}

	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: task.GetId()}))
	if err != nil {
		t.Fatalf("watch DeepSeek task: %v", err)
	}
	var sequence int64
	var startedProvider, assembled, model string
	terminal := 0
	deltas := 0
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		if event.GetSequence() != sequence+1 {
			t.Fatalf("event sequence jumped from %d to %d", sequence, event.GetSequence())
		}
		sequence = event.GetSequence()
		if value := event.GetRunStarted(); value != nil {
			startedProvider = value.GetProviderId()
		}
		if value := event.GetAssistantDelta(); value != nil {
			deltas++
		}
		if value := event.GetAssistantMessage(); value != nil {
			assembled = value.GetText()
		}
		if value := event.GetUsageRecorded(); value != nil {
			model = value.GetModel()
			if value.GetInputTokens() != 9 || value.GetOutputTokens() != 3 || value.GetCostDecimal() != "" {
				t.Fatalf("unexpected persisted usage: %#v", value)
			}
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminal++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("DeepSeek event stream failed: %v", err)
	}
	if startedProvider != "deepseek" || deltas < 2 || assembled != "fixture response" || model != "deepseek-v4-flash" || terminal != 1 {
		t.Fatalf("unexpected DeepSeek stream: provider=%q deltas=%d message=%q model=%q terminal=%d sequence=%d", startedProvider, deltas, assembled, model, terminal, sequence)
	}
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: task.GetId()}))
	if err != nil {
		t.Fatalf("get completed DeepSeek task: %v", err)
	}
	if got := final.Msg.GetTask(); got.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || got.GetProviderId() != "deepseek" || got.GetRunId() == "" {
		t.Fatalf("DeepSeek task snapshot was not durable: %#v", got)
	}

	fakeTask, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "post-rebind-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "prove only new tasks use the rebound fake provider",
		},
	}))
	if err != nil {
		t.Fatalf("submit post-rebind fake task: %v", err)
	}
	if fakeTask.Msg.GetTask().GetProviderId() != "fake" {
		t.Fatalf("new task did not snapshot rebound provider: %#v", fakeTask.Msg.GetTask())
	}
	fakeStream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: fakeTask.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("watch post-rebind fake task: %v", err)
	}
	fakeStarted := ""
	for fakeStream.Receive() {
		if started := fakeStream.Msg().GetEvent().GetRunStarted(); started != nil {
			fakeStarted = started.GetProviderId()
		}
	}
	if err := fakeStream.Err(); err != nil || fakeStarted != "fake" {
		t.Fatalf("post-rebind fake stream failed: provider=%q error=%v", fakeStarted, err)
	}

	for _, failure := range []struct {
		goal      string
		retryable bool
	}{
		{goal: "fixture malformed SSE"},
		{goal: "fixture unexpected content type", retryable: true},
		{goal: "fixture early EOF", retryable: true},
		{goal: "fixture rate limit", retryable: true},
		{goal: "fixture server unavailable", retryable: true},
	} {
		t.Run(failure.goal, func(t *testing.T) {
			assertDeepSeekFixtureFailure(t, ctx, projects, bindings, tasks, failure.goal, failure.retryable)
		})
	}
}

func waitForFixture(t *testing.T, ctx context.Context, client *http.Client) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:18086/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("DeepSeek API fixture did not become ready: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func assertDeepSeekFixtureFailure(
	t *testing.T,
	ctx context.Context,
	projects projectv1connect.ProjectServiceClient,
	bindings projectv1connect.ProjectHarnessBindingServiceClient,
	tasks agentv1connect.AgentTaskServiceClient,
	goal string,
	retryable bool,
) {
	t.Helper()
	key := fmt.Sprintf("deepseek-failure-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "DeepSeek Failure Fixture",
	}))
	if err != nil {
		t.Fatalf("create failure fixture project: %v", err)
	}
	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "deepseek"},
	}))
	if err != nil {
		t.Fatalf("bind failure fixture project: %v", err)
	}
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: bound.Msg.GetProject().GetId()}},
			Role:        "general", Goal: goal, Budget: &agentv1.AgentBudget{MaxTokens: 64, MaxRuntimeSeconds: 20},
		},
	}))
	if err != nil {
		t.Fatalf("submit failure fixture task: %v", err)
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: submitted.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("watch failure fixture task: %v", err)
	}
	startedProvider := ""
	var failed *agentv1.RunFailed
	for stream.Receive() {
		if started := stream.Msg().GetEvent().GetRunStarted(); started != nil {
			startedProvider = started.GetProviderId()
		}
		if value := stream.Msg().GetEvent().GetRunFailed(); value != nil {
			failed = value
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("failure fixture stream: %v", err)
	}
	if startedProvider != "deepseek" || failed == nil || failed.GetReason() == "" || failed.GetRetryable() != retryable || strings.Contains(failed.GetReason(), "workos-fixture-only") {
		t.Fatalf("unexpected failure mapping: provider=%q failed=%#v", startedProvider, failed)
	}
}
