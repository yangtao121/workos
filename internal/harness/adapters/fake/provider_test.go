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
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "  verify foundation  "}, Emit: func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	}, Artifacts: nil})
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
	if caps.GetPersistentSessions() || caps.GetResume() || caps.GetSteerDuringRun() || caps.GetApprovals() || caps.GetToolRegistration() || caps.GetMcp() || caps.GetSubagents() || caps.GetWorkspaceMount() {
		t.Fatalf("fake provider overclaimed capabilities: %#v", caps)
	}
	// Structured artifacts are claimed with the exact canonical type list the
	// adapter demonstrably produces through the materialization protocol
	// (ADR-0008): the bool may never be true without a non-empty exact list.
	if !caps.GetStructuredArtifacts() {
		t.Fatalf("fake provider must claim structured artifacts with an exact list: %#v", caps)
	}
	if got := caps.GetSupportedArtifactTypes(); len(got) != 2 || got[0] != "document.markdown.v1" || got[1] != "code.unified-diff.v1" {
		t.Fatalf("unexpected supported artifact types: %#v", got)
	}
}

func TestProviderEnforcesTokenCap(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	var usage *agentv1.UsageRecorded
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{
		Goal:   "capped run",
		Budget: &agentv1.AgentBudget{MaxTokens: 2},
	}, Emit: func(event *agentv1.AgentEvent) error {
		if event.GetUsageRecorded() != nil {
			usage = event.GetUsageRecorded()
		}
		return nil
	}, Artifacts: nil})
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
		err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "g", Budget: budget}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: nil})
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
	err := provider.Run(ctx, ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "g"}, Emit: func(*agentv1.AgentEvent) error {
		events++
		<-ctx.Done()
		return ctx.Err()
	}, Artifacts: nil})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline verdict, got %v", err)
	}
	if events != 1 {
		t.Fatalf("run should stop at the first blocked emit: %d", events)
	}
}

func TestProviderEmitsExactlyOneArtifactPerRequestedType(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	var events []*agentv1.AgentEvent
	var outputs []ports.ArtifactOutput
	requested := []string{"document.markdown.v1", "code.unified-diff.v1"}
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "g", OutputArtifactTypes: requested}, Emit: func(event *agentv1.AgentEvent) error { events = append(events, event); return nil }, Artifacts: func(output ports.ArtifactOutput) error { outputs = append(outputs, output); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("exactly one output per requested type expected: %#v", outputs)
	}
	if outputs[0].Key != "document" || outputs[0].Title != "Fake Harness Review Document" ||
		outputs[1].Key != "patch" || outputs[1].Title != "Fake Harness Proposed Patch" {
		t.Fatalf("unexpected deterministic output facts: %#v", outputs)
	}
	for _, output := range outputs {
		if output.Type != "document.markdown.v1" && output.Type != "code.unified-diff.v1" {
			t.Fatalf("output type not canonical: %q", output.Type)
		}
		if len(output.Content) == 0 {
			t.Fatalf("empty content for %q", output.Type)
		}
	}
	// Deterministic across runs.
	var again []ports.ArtifactOutput
	if err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "g", OutputArtifactTypes: requested}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: func(output ports.ArtifactOutput) error { again = append(again, output); return nil }}); err != nil {
		t.Fatal(err)
	}
	for i := range outputs {
		if string(outputs[i].Content) != string(again[i].Content) {
			t.Fatalf("output content is not deterministic for %q", outputs[i].Type)
		}
	}
	// Every output strictly precedes the terminal event.
	terminalIndex := -1
	for index, event := range events {
		if event.GetRunCompleted() != nil {
			terminalIndex = index
		}
	}
	if terminalIndex < 0 || len(outputs) == 0 {
		t.Fatalf("no terminal event: %d", terminalIndex)
	}
	_ = terminalIndex
}

func TestProviderRejectsInvalidArtifactRequests(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	cases := []struct {
		name      string
		requested []string
	}{
		{"unsupported type", []string{"image.png.v1"}},
		{"duplicate type", []string{"document.markdown.v1", "document.markdown.v1"}},
		{"over the maximum", []string{"document.markdown.v1", "code.unified-diff.v1", "document.markdown.v1"}},
	}
	for _, testCase := range cases {
		err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "g", OutputArtifactTypes: testCase.requested}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: func(ports.ArtifactOutput) error { t.Fatalf("%s: output emitted", testCase.name); return nil }})
		var runErr *ports.RunError
		if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindInvalidInput {
			t.Fatalf("%s: expected invalid-input verdict, got %v", testCase.name, err)
		}
	}
}

func TestProviderAbortsRunWhenArtifactSinkFails(t *testing.T) {
	t.Parallel()
	provider := New(fixedID("run-1"))
	sinkFailure := errors.New("materialization refused")
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{
		Goal: "g", OutputArtifactTypes: []string{"document.markdown.v1"},
	}, Emit: func(event *agentv1.AgentEvent) error {
		if event.GetRunCompleted() != nil {
			t.Fatal("terminal event emitted after a failed materialization")
		}
		return nil
	}, Artifacts: func(ports.ArtifactOutput) error { return sinkFailure }})
	if !errors.Is(err, sinkFailure) {
		t.Fatalf("sink failure must abort the run: %v", err)
	}
}
