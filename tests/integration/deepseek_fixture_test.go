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
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

func TestDeepSeekProjectBindingFixtureVerticalSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	waitForFixture(t, ctx, client)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	harness := harnessv1connect.NewHarnessHostServiceClient(client, "http://127.0.0.1:8082")

	described, err := harness.DescribeProviders(ctx, connect.NewRequest(&harnessv1.DescribeProvidersRequest{}))
	if err != nil {
		t.Fatalf("describe providers: %v", err)
	}
	deepSeekAvailable := false
	for _, provider := range described.Msg.GetProviders() {
		if provider.GetId() == "deepseek" && provider.GetHealth().String() == "HEALTH_STATE_HEALTHY" && provider.GetCapabilities().GetStreaming() && provider.GetCapabilities().GetUsageReporting() {
			deepSeekAvailable = true
		}
	}
	if !deepSeekAvailable {
		t.Fatalf("DeepSeek fixture provider is not available: %#v", described.Msg.GetProviders())
	}

	key := fmt.Sprintf("deepseek-fixture-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key,
		Name:           "DeepSeek Fixture",
		HarnessBinding: &projectv1.HarnessBinding{
			ProviderId: "deepseek", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL,
			ResourcePolicyId: "fixture-no-tools",
		},
	}))
	if err != nil {
		t.Fatalf("create DeepSeek project: %v", err)
	}
	project := created.Msg.GetProject()
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

	fakeBinding := &projectv1.HarnessBinding{
		ProviderId: "fake", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL,
		ResourcePolicyId: "fixture-no-tools",
	}
	if _, err := projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(), HarnessBinding: fakeBinding,
	})); err != nil {
		t.Fatalf("change project binding after submit: %v", err)
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
			assertDeepSeekFixtureFailure(t, ctx, projects, tasks, failure.goal, failure.retryable)
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
	tasks agentv1connect.AgentTaskServiceClient,
	goal string,
	retryable bool,
) {
	t.Helper()
	key := fmt.Sprintf("deepseek-failure-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "DeepSeek Failure Fixture",
		HarnessBinding: &projectv1.HarnessBinding{
			ProviderId: "deepseek", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL,
			ResourcePolicyId: "fixture-no-tools",
		},
	}))
	if err != nil {
		t.Fatalf("create failure fixture project: %v", err)
	}
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: created.Msg.GetProject().GetId()}},
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
