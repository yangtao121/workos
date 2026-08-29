package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

type fixedID string

func (id fixedID) New() string { return string(id) }

func TestProviderEmitsOneCanonicalTerminalEvent(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	var events []*agentv1.AgentEvent
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "  verify foundation  "}, func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminal++
		}
	}
	if len(events) != 5 || terminal != 1 || events[0].GetRunStarted().GetRunId() != "run-1" {
		t.Fatalf("unexpected fake event sequence: count=%d terminal=%d", len(events), terminal)
	}
	if got := events[2].GetAssistantMessage().GetText(); got != "verify foundation" {
		t.Fatalf("unexpected normalized goal %q", got)
	}
}

func TestProviderAdvertisesOnlyImplementedCapabilities(t *testing.T) {
	t.Parallel()
	caps := New(fixedID("run-1")).Describe().GetCapabilities()
	// The budget contract claims are backed by TestProviderEnforcesTokenCap
	// and the cancellation stop below (ADR-0005 §5).
	if !caps.GetStreaming() || !caps.GetUsageReporting() || !caps.GetHardTokenBudget() || !caps.GetHardRuntimeDeadline() {
		t.Fatalf("fake provider underclaimed implemented capabilities: %#v", caps)
	}
	if caps.GetPersistentSessions() || caps.GetResume() || caps.GetSteerDuringRun() || caps.GetApprovals() || caps.GetToolRegistration() || caps.GetMcp() || caps.GetSubagents() || caps.GetWorkspaceMount() || caps.GetStructuredArtifacts() {
		t.Fatalf("fake provider overclaimed capabilities: %#v", caps)
	}
}

func TestProviderEnforcesTokenCap(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	var usage *agentv1.UsageRecorded
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{
		Goal:   "capped run",
		Budget: &agentv1.AgentBudget{MaxTokens: 2},
	}, func(event *agentv1.AgentEvent) error {
		if event.GetUsageRecorded() != nil {
			usage = event.GetUsageRecorded()
		}
		return nil
	})
	if err != nil || usage == nil || usage.GetOutputTokens() != 2 {
		t.Fatalf("token cap not enforced: %v %#v", err, usage)
	}
}

func TestProviderRejectsInvalidBudgets(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	cases := map[string]*agentv1.AgentBudget{
		"overbound tokens":        {MaxTokens: 1_000_001},
		"overbound runtime":       {MaxRuntimeSeconds: 86_401},
		"cost budget unsupported": {MaxCostDecimal: "0.25"},
	}
	for name, budget := range cases {
		err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "g", Budget: budget}, func(*agentv1.AgentEvent) error { return nil })
		var runErr *ports.RunError
		if err == nil || !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindInvalidInput {
			t.Fatalf("%s: expected invalid-input verdict, got %v", name, err)
		}
	}
}

func TestProviderStopsOnContextDeadline(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	events := 0
	err := provider.Run(ctx, "task-1", &agentv1.AgentTaskInput{Goal: "g"}, func(*agentv1.AgentEvent) error {
		events++
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline verdict, got %v", err)
	}
	if events != 1 {
		t.Fatalf("run should stop at the first blocked emit: %d", events)
	}
}
