//go:build integration && mcpfixture

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

// TestMCPProjectBindingFixtureVerticalSlice proves the MCP chain
// (ADR-0015) across the real stack with the honest degraded capability
// subset: catalog projection, credential-free binding, deterministic
// blocking tool-call run, and durable completion.
func TestMCPProjectBindingFixtureVerticalSlice(t *testing.T) {
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
	mcpAvailable := false
	for _, provider := range described.Msg.GetProviders() {
		if provider.GetId() == "mcp" {
			capabilities := provider.GetCapabilities()
			// The honest degraded subset: none of the rich capabilities may
			// be claimed, and no credential lease is required.
			if provider.GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY &&
				!capabilities.GetStreaming() && !capabilities.GetUsageReporting() &&
				!capabilities.GetHardTokenBudget() && !capabilities.GetHardRuntimeDeadline() &&
				!capabilities.GetRequiresTaskCredentialLease() {
				mcpAvailable = true
			}
		}
	}
	if !mcpAvailable {
		t.Fatalf("MCP fixture provider is not available: %#v", described.Msg.GetProviders())
	}

	key := fmt.Sprintf("mcp-fixture-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "MCP Fixture",
	}))
	if err != nil {
		t.Fatalf("create MCP project: %v", err)
	}
	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "mcp"},
	}))
	if err != nil {
		t.Fatalf("bind MCP through public orchestration: %v", err)
	}
	project := bound.Msg.GetProject()
	if binding := project.GetHarnessBinding(); binding.GetProviderId() != "mcp" || binding.GetCredentialRef() != "" {
		t.Fatalf("MCP binding must stay credential-free: %#v", binding)
	}

	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "prove the mcp project binding fixture",
		},
	}))
	if err != nil {
		t.Fatalf("submit MCP task: %v", err)
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: submitted.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("watch MCP task: %v", err)
	}
	startedProvider, assembled := "", ""
	terminal := 0
	usage := 0
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		if value := event.GetRunStarted(); value != nil {
			startedProvider = value.GetProviderId()
		}
		if value := event.GetAssistantMessage(); value != nil {
			assembled = value.GetText()
		}
		if event.GetUsageRecorded() != nil {
			usage++
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminal++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("MCP event stream failed: %v", err)
	}
	if startedProvider != "mcp" || assembled != "mcp fixture result for: prove the mcp project binding fixture" || terminal != 1 {
		t.Fatalf("unexpected MCP stream: provider=%q message=%q terminal=%d", startedProvider, assembled, terminal)
	}
	if usage != 0 {
		t.Fatalf("a usage-reporting-less provider must not emit usage events: %d", usage)
	}
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: submitted.Msg.GetTask().GetId()}))
	if err != nil {
		t.Fatalf("get completed MCP task: %v", err)
	}
	if got := final.Msg.GetTask(); got.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || got.GetProviderId() != "mcp" {
		t.Fatalf("MCP task snapshot was not durable: %#v", got)
	}
}
