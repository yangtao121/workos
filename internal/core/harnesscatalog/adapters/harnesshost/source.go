package harnesshost

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type describeClient interface {
	DescribeProviders(context.Context, *connect.Request[harnessv1.DescribeProvidersRequest]) (*connect.Response[harnessv1.DescribeProvidersResponse], error)
}

type Source struct {
	client  describeClient
	timeout time.Duration
}

func New(client describeClient, timeout time.Duration) (*Source, error) {
	if client == nil || timeout <= 0 {
		return nil, errors.New("harness catalog source requires a client and timeout")
	}
	return &Source{client: client, timeout: timeout}, nil
}

func (s *Source) ListProviders(ctx context.Context) ([]domain.Provider, error) {
	requestContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	response, err := s.client.DescribeProviders(requestContext, connect.NewRequest(&harnessv1.DescribeProvidersRequest{}))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if requestContext.Err() != nil {
			return nil, requestContext.Err()
		}
		return nil, domain.ErrUnavailable
	}
	if response == nil || response.Msg == nil {
		return nil, domain.ErrUnavailable
	}
	providers := make([]domain.Provider, 0, len(response.Msg.GetProviders()))
	for _, provider := range response.Msg.GetProviders() {
		if provider == nil {
			providers = append(providers, domain.Provider{})
			continue
		}
		capabilities := provider.GetCapabilities()
		providers = append(providers, domain.Provider{
			ID:             provider.GetId(),
			DisplayName:    provider.GetDisplayName(),
			AdapterVersion: provider.GetAdapterVersion(),
			Health:         healthFromProto(provider.GetHealth()),
			Capabilities: domain.Capabilities{
				Streaming: capabilities.GetStreaming(), PersistentSessions: capabilities.GetPersistentSessions(),
				Resume: capabilities.GetResume(), SteerDuringRun: capabilities.GetSteerDuringRun(),
				Approvals: capabilities.GetApprovals(), ToolRegistration: capabilities.GetToolRegistration(),
				MCP: capabilities.GetMcp(), Subagents: capabilities.GetSubagents(),
				WorkspaceMount: capabilities.GetWorkspaceMount(), StructuredArtifacts: capabilities.GetStructuredArtifacts(),
				UsageReporting:  capabilities.GetUsageReporting(),
				HardTokenBudget: capabilities.GetHardTokenBudget(), HardRuntimeDeadline: capabilities.GetHardRuntimeDeadline(),
				MaxOutputTokens:             capabilities.GetMaxOutputTokens(),
				MaxRuntimeSeconds:           capabilities.GetMaxRuntimeSeconds(),
				SupportedArtifactTypes:      capabilities.GetSupportedArtifactTypes(),
				SupportedContextRefTypes:    capabilities.GetSupportedContextRefTypes(),
				RequiresTaskCredentialLease: capabilities.GetRequiresTaskCredentialLease(),
			},
		})
	}
	return providers, nil
}

func healthFromProto(value commonv1.HealthState) domain.Health {
	switch value {
	case commonv1.HealthState_HEALTH_STATE_STARTING:
		return domain.HealthStarting
	case commonv1.HealthState_HEALTH_STATE_HEALTHY:
		return domain.HealthHealthy
	case commonv1.HealthState_HEALTH_STATE_DEGRADED:
		return domain.HealthDegraded
	case commonv1.HealthState_HEALTH_STATE_UNAVAILABLE:
		return domain.HealthUnavailable
	default:
		return domain.HealthUnknown
	}
}
