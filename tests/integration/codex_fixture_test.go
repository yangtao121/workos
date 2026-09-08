//go:build integration && codexfixture

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
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

// TestCodexProjectBindingFixtureVerticalSlice proves the Codex chain
// (ADR-0015) across the real stack: catalog honesty, credential-lease
// binding, provider snapshot, streaming canonical events with bounded usage,
// and idempotent replay — against harness-host running the versioned
// app-server fixture.
func TestCodexProjectBindingFixtureVerticalSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	catalogs := harnessv1connect.NewHarnessCatalogServiceClient(client, baseURL)

	described, err := catalogs.GetHarnessCatalog(ctx, connect.NewRequest(&harnessv1.GetHarnessCatalogRequest{}))
	if err != nil {
		t.Fatalf("get public provider catalog: %v", err)
	}
	codexAvailable := false
	for _, provider := range described.Msg.GetProviders() {
		if provider.GetId() == "codex" {
			capabilities := provider.GetCapabilities()
			if provider.GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY && capabilities.GetStreaming() &&
				capabilities.GetUsageReporting() && capabilities.GetRequiresTaskCredentialLease() &&
				capabilities.GetHardTokenBudget() && capabilities.GetHardRuntimeDeadline() {
				codexAvailable = true
			}
		}
	}
	if !codexAvailable {
		t.Fatalf("Codex fixture provider is not available: %#v", described.Msg.GetProviders())
	}

	key := fmt.Sprintf("codex-fixture-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Codex Fixture",
	}))
	if err != nil {
		t.Fatalf("create Codex project: %v", err)
	}
	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "codex"},
	}))
	if err != nil {
		t.Fatalf("bind Codex through public orchestration: %v", err)
	}
	project := bound.Msg.GetProject()
	// The codex-auth.v1 credential the operator stored must surface as the
	// server-derived opaque credential_ref (ADR-0009/0015).
	binding := project.GetHarnessBinding()
	if project.GetRevision() != 2 || binding.GetProviderId() != "codex" || len(binding.GetCredentialRef()) != 36 {
		t.Fatalf("unexpected server-owned Codex binding: %#v", project)
	}
	taskKey := "task-" + key
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "prove the codex project binding fixture",
			Budget: &agentv1.AgentBudget{MaxTokens: 64, MaxRuntimeSeconds: 20},
		},
	}))
	if err != nil {
		t.Fatalf("submit Codex task: %v", err)
	}
	task := submitted.Msg.GetTask()
	if task.GetProviderId() != "codex" {
		t.Fatalf("task did not snapshot Codex binding: %#v", task)
	}
	// Since the caller-input idempotency binding, a same-key replay with a
	// different payload is a stable Aborted (the first Codex snapshot is
	// immutable); the identical payload still replays the exact task.
	changed, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "changed retry payload must be refused",
		},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("changed replay payload must abort, got %#v err=%v", changed, err)
	}
	repeated, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "prove the codex project binding fixture", Budget: &agentv1.AgentBudget{MaxTokens: 64, MaxRuntimeSeconds: 20},
		},
	}))
	if err != nil || repeated.Msg.GetTask().GetId() != task.GetId() || repeated.Msg.GetTask().GetProviderId() != "codex" {
		t.Fatalf("identical replay did not preserve the Codex snapshot: %#v err=%v", repeated.Msg.GetTask(), err)
	}

	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: task.GetId()}))
	if err != nil {
		t.Fatalf("watch Codex task: %v", err)
	}
	var sequence int64
	startedProvider, assembled := "", ""
	outputTokens := int64(-1)
	terminal, deltas := 0, 0
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
			outputTokens = value.GetOutputTokens()
			if value.GetInputTokens() != 0 {
				t.Fatalf("unexpected persisted usage: %#v", value)
			}
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminal++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("Codex event stream failed: %v", err)
	}
	// The fixture reports 12+len(goal)%7 output tokens for this goal and
	// never exceeds the caller's hard budget.
	if startedProvider != "codex" || deltas < 2 || assembled != "codex fixture reviewed: prove the codex project binding fixture" {
		t.Fatalf("unexpected Codex stream: provider=%q deltas=%d message=%q", startedProvider, deltas, assembled)
	}
	if outputTokens != 12+int64(len("prove the codex project binding fixture")%7) || outputTokens > 64 {
		t.Fatalf("unexpected Codex usage: %d", outputTokens)
	}
	if terminal != 1 {
		t.Fatalf("expected exactly one terminal event, got %d", terminal)
	}
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: task.GetId()}))
	if err != nil {
		t.Fatalf("get completed Codex task: %v", err)
	}
	if got := final.Msg.GetTask(); got.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || got.GetProviderId() != "codex" || got.GetRunId() == "" {
		t.Fatalf("Codex task snapshot was not durable: %#v", got)
	}
}
