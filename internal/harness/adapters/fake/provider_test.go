package fake

import (
	"context"
	"testing"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
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
	if !caps.GetStreaming() || !caps.GetUsageReporting() || caps.GetPersistentSessions() || caps.GetResume() || caps.GetSteerDuringRun() || caps.GetApprovals() || caps.GetToolRegistration() || caps.GetMcp() || caps.GetSubagents() || caps.GetWorkspaceMount() || caps.GetStructuredArtifacts() {
		t.Fatalf("fake provider overclaimed capabilities: %#v", caps)
	}
}
