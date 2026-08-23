package ports

import (
	"context"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
)

type Emit func(*agentv1.AgentEvent) error

type Provider interface {
	Describe() *harnessv1.HarnessProviderInfo
	Run(context.Context, string, *agentv1.AgentTaskInput, Emit) error
}
