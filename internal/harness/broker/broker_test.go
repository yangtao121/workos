package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

type blockingProvider struct{ started chan struct{} }

func (provider *blockingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "blocking", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (provider *blockingProvider) Run(ctx context.Context, _ ports.Execution) error {
	close(provider.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestCancelStopsOnlyTheAddressedRun(t *testing.T) {
	t.Parallel()
	provider := &blockingProvider{started: make(chan struct{})}
	value := New(provider)
	result := make(chan error, 1)
	go func() {
		result <- value.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: nil}, "blocking")
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	if !value.Cancel("task-1") || value.Cancel("other-task") {
		t.Fatal("unexpected cancellation routing")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled provider, got %v", err)
	}
}

func TestUnknownProviderIsExplicitlyUnavailable(t *testing.T) {
	t.Parallel()
	err := New().Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: nil}, "missing")
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected unavailable provider, got %v", err)
	}
}
