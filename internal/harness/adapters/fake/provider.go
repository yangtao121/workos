package fake

import (
	"context"
	"fmt"
	"strings"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type Provider struct{ ids ids.Generator }

func New(generator ids.Generator) *Provider { return &Provider{ids: generator} }

func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{
		Id: "fake", DisplayName: "Deterministic Fake Harness", AdapterVersion: "1.0.0",
		Health:       commonv1.HealthState_HEALTH_STATE_HEALTHY,
		Capabilities: &harnessv1.HarnessCapabilities{Streaming: true, UsageReporting: true},
	}
}

func (p *Provider) Run(ctx context.Context, taskID string, input *agentv1.AgentTaskInput, emit ports.Emit) error {
	runID := p.ids.New()
	events := []*agentv1.AgentEvent{
		{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{RunId: runID, ProviderId: "fake"}}},
		{Event: &agentv1.AgentEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{Text: "Fake harness received: "}}},
		{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: strings.TrimSpace(input.GetGoal())}}},
		{Event: &agentv1.AgentEvent_UsageRecorded{UsageRecorded: &agentv1.UsageRecorded{InputTokens: int64(len([]rune(input.GetGoal()))), OutputTokens: 4, Model: "fake/deterministic"}}},
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
