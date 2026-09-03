// Package codex adapts the Codex App Server to the canonical harness
// provider contract (ADR-0015). Every Codex protocol detail stays inside
// this package: Core only ever sees canonical events, the neutral lease, and
// the honest capability description. The child is one app-server process per
// run, initialized with an explicit protocol version, killed by the derived
// hard runtime deadline, and entitled solely through this task's credential
// lease environment.
package codex

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

// Provider is the Codex harness adapter.
type Provider struct {
	config Config
	ids    ids.Generator

	mu     sync.RWMutex
	health commonv1.HealthState
	reason string
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

// Describe declares exactly what the adapter proves. Structured artifacts,
// context refs, steering, and approvals are unsupported: the Codex fixture
// contract has none of them, so any request for them fails closed in Run.
func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	p.mu.RLock()
	health, reason := p.health, p.reason
	p.mu.RUnlock()
	return &harnessv1.HarnessProviderInfo{
		Id:                ProviderID,
		DisplayName:       "Codex Harness",
		AdapterVersion:    AdapterVersion,
		Health:            health,
		UnavailableReason: reason,
		Capabilities: &harnessv1.HarnessCapabilities{
			// Proven by the fixture streaming tests: events arrive as an
			// ordered started → delta* → message → usage → completed stream.
			Streaming:      true,
			UsageReporting: true,
			// Proven by the over-budget and slow fixture modes: the app
			// server enforces the caller's max_tokens cap and the adapter
			// terminates the child at its hard process deadline.
			HardTokenBudget:     true,
			HardRuntimeDeadline: true,
			MaxOutputTokens:     MaximumMaxTokens,
			MaxRuntimeSeconds:   int64(MaximumTimeout / time.Second),
			// ADR-0015: the adapter never holds long-lived Codex material;
			// every run requires a codex-auth.v1 task-bound credential lease.
			RequiresTaskCredentialLease: true,
			// ADR-0015: the exact canonical kind every run acquires.
			RequiredCredentialPurpose: purpose,
		},
	}
}

func (p *Provider) setHealth(health commonv1.HealthState, reason string) {
	p.mu.Lock()
	p.health, p.reason = health, reason
	p.mu.Unlock()
}

// Run executes one canonical task against the app server. The lease is
// mandatory and must bind codex + codex-auth.v1; the resolved artifact and
// context protocols are out of contract and refused outright.
func (p *Provider) Run(ctx context.Context, execution ports.Execution) error {
	taskID, input, emit := execution.TaskID, execution.Input, execution.Emit
	_ = execution.Artifacts
	if err := validateConfig(p.config); err != nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, err.Error())
		return ports.NewRunError(ports.ErrorKindConfiguration, err.Error(), false, nil)
	}
	if !execution.Credential.ValidFor(ProviderID, purpose, time.Now()) {
		return ports.NewRunError(ports.ErrorKindConfiguration, "provider credential lease is missing, expired, or bound to another provider", false, nil)
	}
	lease := execution.Credential
	defer func() {
		for index := range lease.Secret {
			lease.Secret[index] = 0
		}
	}()
	if execution.Credential.Purpose != purpose {
		return ports.NewRunError(ports.ErrorKindConfiguration, "codex requires a codex-auth.v1 credential lease", false, nil)
	}
	prepared, err := prepareInput(input)
	if err != nil {
		return err
	}
	runID := p.ids.New()
	err = p.execute(ctx, taskID, runID, prepared, lease, emit)
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

type preparedInput struct {
	goal      string
	maxTokens int64
}

// prepareInput enforces the canonical input grammar: bounded UTF-8 goal,
// general role only, no requested capabilities, no artifact outputs, and a
// token budget within the declared enforced maximum.
func prepareInput(input *agentv1.AgentTaskInput) (preparedInput, error) {
	if input == nil {
		return preparedInput{}, invalidInput("Codex task input is required")
	}
	goal := input.GetGoal()
	if strings.TrimSpace(goal) == "" {
		return preparedInput{}, invalidInput("Codex task goal is required")
	}
	if len(goal) > maximumGoalBytes {
		return preparedInput{}, invalidInput("Codex task goal exceeds the supported size")
	}
	if role := input.GetRole(); role != "" && role != "general" {
		return preparedInput{}, invalidInput("Codex Harness supports only the general role")
	}
	if len(input.GetRequestedCapabilities()) != 0 {
		return preparedInput{}, invalidInput("Codex Harness does not support requested capabilities")
	}
	if len(input.GetOutputArtifactTypes()) != 0 {
		return preparedInput{}, invalidInput("Codex Harness does not support structured artifacts")
	}
	if len(input.GetContextRefs()) != 0 {
		return preparedInput{}, invalidInput("Codex Harness does not support context references")
	}
	maxTokens := DefaultMaxTokens
	if budget := input.GetBudget(); budget != nil {
		if budget.GetMaxCostDecimal() != "" {
			return preparedInput{}, invalidInput("Codex Harness does not support cost budgets")
		}
		if budget.GetMaxTokens() < 0 || budget.GetMaxTokens() > MaximumMaxTokens {
			return preparedInput{}, invalidInput(fmt.Sprintf("Codex max_tokens must be between 1 and %d", MaximumMaxTokens))
		}
		if budget.GetMaxTokens() > 0 {
			maxTokens = budget.GetMaxTokens()
		}
	}
	return preparedInput{goal: goal, maxTokens: maxTokens}, nil
}

func invalidInput(reason string) error {
	return ports.NewRunError(ports.ErrorKindInvalidInput, reason, false, nil)
}
