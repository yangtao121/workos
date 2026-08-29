package fake

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// The fake provider's deterministic output budget: the canonical event stream
// reports exactly four output tokens. A budget cap below that bound trims the
// reported usage to the cap, which is how this adapter demonstrably enforces
// a hard token budget.
const deterministicOutputTokens = 4

// maxOutputTokenCap bounds the accepted AgentBudget.max_tokens; anything
// above it is rejected as invalid input instead of silently ignored.
const maxOutputTokenCap = 1_000_000

// maxRuntimeSecondsCap bounds the accepted AgentBudget.max_runtime_seconds.
const maxRuntimeSecondsCap = 86_400

type Provider struct{ ids ids.Generator }

func New(generator ids.Generator) *Provider { return &Provider{ids: generator} }

func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{
		Id: "fake", DisplayName: "Deterministic Fake Harness", AdapterVersion: "1.0.0",
		Health: commonv1.HealthState_HEALTH_STATE_HEALTHY,
		Capabilities: &harnessv1.HarnessCapabilities{
			Streaming: true, UsageReporting: true,
			// The fake adapter enforces the budget contract deterministically:
			// the run stops on context cancellation (deadline or cancel) and
			// the reported usage never exceeds an accepted token cap.
			HardTokenBudget:     true,
			HardRuntimeDeadline: true,
		},
	}
}

func (p *Provider) Run(ctx context.Context, taskID string, input *agentv1.AgentTaskInput, emit ports.Emit) error {
	outputTokens, err := p.validateBudget(input.GetBudget())
	if err != nil {
		return err
	}
	runID := p.ids.New()
	events := []*agentv1.AgentEvent{
		{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{RunId: runID, ProviderId: "fake"}}},
		{Event: &agentv1.AgentEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{Text: "Fake harness received: "}}},
		{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: strings.TrimSpace(input.GetGoal())}}},
		{Event: &agentv1.AgentEvent_UsageRecorded{UsageRecorded: &agentv1.UsageRecorded{
			InputTokens: int64(len([]rune(input.GetGoal()))), OutputTokens: outputTokens, Model: "fake/deterministic",
		}}},
		{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: fmt.Sprintf("Task %s completed by fake harness", taskID)}}},
	}
	for _, event := range events {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

// validateBudget enforces the accepted budget contract: unset fields keep the
// deterministic defaults, set fields must be inside their fixed bounds, and
// cost budgets are not supported by this adapter and are refused outright.
func (p *Provider) validateBudget(budget *agentv1.AgentBudget) (int64, error) {
	if budget == nil {
		return deterministicOutputTokens, nil
	}
	outputTokens := int64(deterministicOutputTokens)
	if cap := budget.GetMaxTokens(); cap > 0 {
		if cap > maxOutputTokenCap {
			return 0, ports.NewRunError(ports.ErrorKindInvalidInput, "budget", false,
				fmt.Errorf("max_tokens %d exceeds the fake harness cap", cap))
		}
		outputTokens = min(cap, deterministicOutputTokens)
	}
	if seconds := budget.GetMaxRuntimeSeconds(); seconds > maxRuntimeSecondsCap {
		return 0, ports.NewRunError(ports.ErrorKindInvalidInput, "budget", false,
			fmt.Errorf("max_runtime_seconds %d exceeds the fake harness cap", seconds))
	}
	if budget.GetMaxCostDecimal() != "" {
		return 0, ports.NewRunError(ports.ErrorKindInvalidInput, "budget", false,
			errors.New("cost budgets are not supported by the fake harness"))
	}
	return outputTokens, nil
}
