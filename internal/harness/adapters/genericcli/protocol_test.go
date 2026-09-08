package genericcli

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

func TestStructuredOutputsPublishOnlyAfterCompleteSuccess(t *testing.T) {
	for _, mode := range []string{"structured-valid", "structured-exit-failure", "structured-failed", "structured-missing", "structured-duplicate", "structured-after-terminal", "structured-before-start", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			provider := helperProvider(t, mode, time.Duration(helperTimeoutScale)*time.Second)
			published, completed := 0, false
			execution := ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{OutputArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"}}, Emit: func(event *agentv1.AgentEvent) error {
				if event.GetRunCompleted() != nil {
					if published != 2 {
						t.Error("completion preceded batch publication")
					}
					completed = true
				}
				return nil
			}, ArtifactsBatch: func(outputs []ports.ArtifactOutput) error { published += len(outputs); return nil }}
			err := provider.Run(context.Background(), execution)
			if mode == "structured-valid" {
				if err != nil || !completed || published != 2 {
					t.Fatalf("valid output failed: %v completed=%v published=%d", err, completed, published)
				}
			} else {
				if completed || published != 0 {
					t.Fatalf("failed output published: %v completed=%v published=%d", err, completed, published)
				}
				if mode == "structured-failed" && err != nil {
					t.Fatalf("valid failed terminal rejected: %v", err)
				}
				if mode != "structured-failed" && err == nil {
					t.Fatal("invalid stream succeeded")
				}
			}
		})
	}
}
func TestStructuredSinkFailurePreventsCompletion(t *testing.T) {
	provider := helperProvider(t, "structured-valid", time.Duration(helperTimeoutScale)*time.Second)
	failure := errors.New("fixture sink failure")
	completed := false
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{OutputArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"}}, Emit: func(event *agentv1.AgentEvent) error {
		completed = completed || event.GetRunCompleted() != nil
		return nil
	}, ArtifactsBatch: func([]ports.ArtifactOutput) error { return failure }})
	if !errors.Is(err, failure) || completed {
		t.Fatalf("sink failure ignored: %v completed=%v", err, completed)
	}
}
func TestCLIReceivesOnlyExactlyBoundContext(t *testing.T) {
	provider := helperProvider(t, "structured-context", time.Duration(helperTimeoutScale)*time.Second)
	execution := ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{ContextRefs: []*agentv1.ContextRef{{Type: "artifact.review.v1", Id: "artifact-1", Revision: "sha256:fixture"}}, OutputArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"}}, Context: []ports.ContextDocument{{RefType: "artifact.review.v1", ArtifactType: "document.markdown.v1", ArtifactID: "artifact-1", Digest: "sha256:fixture", Content: []byte("synthetic pinned context")}}, Emit: func(*agentv1.AgentEvent) error { return nil }, ArtifactsBatch: func([]ports.ArtifactOutput) error { return nil }}
	if err := provider.Run(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	execution.Context[0].Digest = "changed"
	if err := provider.Run(context.Background(), execution); err == nil {
		t.Fatal("unbound context reached child")
	}
}
func TestCLIEnforcesRequestedRuntimeDeadline(t *testing.T) {
	provider := helperProvider(t, "timeout-long", 10*time.Second)
	started := time.Now()
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Budget: &agentv1.AgentBudget{MaxRuntimeSeconds: 1}}, Emit: func(*agentv1.AgentEvent) error { return nil }})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("task deadline not enforced: %v elapsed=%v", err, time.Since(started))
	}
}
func TestCLIRejectsUnsupportedBudgetsAndMissingSinksBeforeExecution(t *testing.T) {
	provider, err := New(Config{Executable: "/unavailable/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []*agentv1.AgentTaskInput{
		{Budget: &agentv1.AgentBudget{MaxTokens: 1}},
		{Budget: &agentv1.AgentBudget{MaxCostDecimal: "1"}},
		{Budget: &agentv1.AgentBudget{MaxRuntimeSeconds: -1}},
		{OutputArtifactTypes: []string{"document.markdown.v1"}},
		{OutputArtifactTypes: []string{"unknown"}},
	} {
		err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: input, Emit: func(*agentv1.AgentEvent) error { return nil }})
		var runErr *ports.RunError
		if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindInvalidInput {
			t.Fatalf("unsupported request executed: %v", err)
		}
	}
}
