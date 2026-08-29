package deepseek

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type Provider struct {
	config Config
	ids    ids.Generator

	mu     sync.RWMutex
	health commonv1.HealthState
	reason string
}

type preparedInput struct {
	goal      string
	maxTokens int64
	timeout   time.Duration
}

func New(config Config, generator ids.Generator) *Provider {
	config = normalizeConfig(config)
	provider := &Provider{config: config, ids: generator}
	if err := validateConfig(config); err != nil {
		provider.health = commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
		provider.reason = err.Error()
	} else {
		provider.health = commonv1.HealthState_HEALTH_STATE_HEALTHY
	}
	return provider
}

func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	p.mu.RLock()
	health, reason := p.health, p.reason
	p.mu.RUnlock()
	return &harnessv1.HarnessProviderInfo{
		Id:                ProviderID,
		DisplayName:       "DeepSeek Harness",
		AdapterVersion:    AdapterVersion,
		Health:            health,
		UnavailableReason: reason,
		Capabilities: &harnessv1.HarnessCapabilities{
			Streaming:      true,
			UsageReporting: true,
			// The pinned runtime enforces max_tokens as a real provider cap
			// and the adapter maps max_runtime_seconds onto a hard process
			// deadline; tests prove both contracts (ADR-0005).
			HardTokenBudget:     true,
			HardRuntimeDeadline: true,
			// The enforced maxima prepareInput refuses budgets beyond, so Core
			// can reject over-bound policies before queueing or reserving.
			MaxOutputTokens:   MaximumMaxTokens,
			MaxRuntimeSeconds: int64(MaximumTimeout / time.Second),
		},
	}
}

func (p *Provider) Run(ctx context.Context, taskID string, input *agentv1.AgentTaskInput, emit ports.Emit) error {
	if err := validateConfig(p.config); err != nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, err.Error())
		return ports.NewRunError(ports.ErrorKindConfiguration, err.Error(), false, nil)
	}
	prepared, err := prepareInput(input, p.config.Timeout)
	if err != nil {
		return err
	}
	runID := p.ids.New()
	err = p.execute(ctx, taskID, runID, prepared, emit)
	if err == nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_HEALTHY, "")
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var runErr *ports.RunError
	if errors.As(err, &runErr) {
		switch runErr.Kind {
		case ports.ErrorKindAuthentication, ports.ErrorKindConfiguration:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, runErr.Error())
		case ports.ErrorKindRateLimit, ports.ErrorKindProvider, ports.ErrorKindTransport, ports.ErrorKindTimeout:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_DEGRADED, runErr.Error())
		}
	}
	return err
}

func (p *Provider) setHealth(health commonv1.HealthState, reason string) {
	p.mu.Lock()
	p.health, p.reason = health, reason
	p.mu.Unlock()
}

func prepareInput(input *agentv1.AgentTaskInput, configuredTimeout time.Duration) (preparedInput, error) {
	if input == nil {
		return preparedInput{}, invalidInput("DeepSeek task input is required")
	}
	if strings.TrimSpace(input.GetGoal()) == "" {
		return preparedInput{}, invalidInput("DeepSeek task goal is required")
	}
	if len(input.GetGoal()) > maximumGoalBytes {
		return preparedInput{}, invalidInput("DeepSeek task goal exceeds the supported size")
	}
	role := input.GetRole()
	if role != "" && role != "general" {
		return preparedInput{}, invalidInput("DeepSeek Harness supports only the general role")
	}
	if len(input.GetContextRefs()) != 0 {
		return preparedInput{}, invalidInput("DeepSeek Harness does not support context references")
	}
	if len(input.GetRequestedCapabilities()) != 0 {
		return preparedInput{}, invalidInput("DeepSeek Harness does not support requested capabilities")
	}
	if len(input.GetOutputArtifactTypes()) != 0 {
		return preparedInput{}, invalidInput("DeepSeek Harness does not support structured artifacts")
	}

	maxTokens, timeout := DefaultMaxTokens, configuredTimeout
	if budget := input.GetBudget(); budget != nil {
		if budget.GetMaxCostDecimal() != "" {
			return preparedInput{}, invalidInput("DeepSeek Harness does not support cost budgets")
		}
		if budget.GetMaxTokens() < 0 || budget.GetMaxTokens() > MaximumMaxTokens {
			return preparedInput{}, invalidInput(fmt.Sprintf("DeepSeek max_tokens must be between 1 and %d", MaximumMaxTokens))
		}
		if budget.GetMaxTokens() > 0 {
			maxTokens = budget.GetMaxTokens()
		}
		if budget.GetMaxRuntimeSeconds() < 0 || budget.GetMaxRuntimeSeconds() > int64(MaximumTimeout/time.Second) {
			return preparedInput{}, invalidInput("DeepSeek max_runtime_seconds must be between 1 and 600")
		}
		if budget.GetMaxRuntimeSeconds() > 0 {
			requested := time.Duration(budget.GetMaxRuntimeSeconds()) * time.Second
			if requested < timeout {
				timeout = requested
			}
		}
	}
	return preparedInput{goal: input.GetGoal(), maxTokens: maxTokens, timeout: timeout}, nil
}

func invalidInput(reason string) error {
	return ports.NewRunError(ports.ErrorKindInvalidInput, reason, false, nil)
}
