package broker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

var ErrProviderUnavailable = errors.New("harness provider is unavailable")

type Broker struct {
	mu        sync.RWMutex
	providers map[string]ports.Provider
	runs      map[string]context.CancelFunc
}

func New(providers ...ports.Provider) *Broker {
	b := &Broker{providers: make(map[string]ports.Provider), runs: make(map[string]context.CancelFunc)}
	for _, provider := range providers {
		b.providers[provider.Describe().GetId()] = provider
	}
	return b
}

func (b *Broker) Describe() []*harnessv1.HarnessProviderInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]*harnessv1.HarnessProviderInfo, 0, len(b.providers))
	for _, provider := range b.providers {
		result = append(result, provider.Describe())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GetId() < result[j].GetId() })
	return result
}

func (b *Broker) Run(ctx context.Context, taskID, providerID string, input *agentv1.AgentTaskInput, emit ports.Emit) error {
	b.mu.RLock()
	provider, ok := b.providers[providerID]
	b.mu.RUnlock()
	if !ok {
		return ports.NewRunError(ports.ErrorKindUnavailable, fmt.Sprintf("harness provider %q is not registered", providerID), false, ErrProviderUnavailable)
	}
	description := provider.Describe()
	switch description.GetHealth() {
	case commonv1.HealthState_HEALTH_STATE_HEALTHY, commonv1.HealthState_HEALTH_STATE_DEGRADED:
	case commonv1.HealthState_HEALTH_STATE_STARTING:
		return ports.NewRunError(ports.ErrorKindUnavailable, "harness provider is starting", true, ErrProviderUnavailable)
	default:
		reason := description.GetUnavailableReason()
		if reason == "" {
			reason = "harness provider is unavailable"
		}
		return ports.NewRunError(ports.ErrorKindUnavailable, reason, false, ErrProviderUnavailable)
	}
	runCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.runs[taskID] = cancel
	b.mu.Unlock()
	defer func() {
		cancel()
		b.mu.Lock()
		delete(b.runs, taskID)
		b.mu.Unlock()
	}()
	return provider.Run(runCtx, taskID, input, emit)
}

func (b *Broker) Cancel(taskID string) bool {
	b.mu.RLock()
	cancel, ok := b.runs[taskID]
	b.mu.RUnlock()
	if ok {
		cancel()
	}
	return ok
}
