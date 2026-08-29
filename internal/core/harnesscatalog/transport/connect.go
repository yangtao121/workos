package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/application"
	"github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
)

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

func (h *Handler) GetHarnessCatalog(ctx context.Context, _ *connect.Request[harnessv1.GetHarnessCatalogRequest]) (*connect.Response[harnessv1.GetHarnessCatalogResponse], error) {
	catalog, err := h.service.Get(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	providers := make([]*harnessv1.HarnessProviderInfo, 0, len(catalog.Providers))
	for _, provider := range catalog.Providers {
		providers = append(providers, &harnessv1.HarnessProviderInfo{
			Id: provider.ID, DisplayName: provider.DisplayName, AdapterVersion: provider.AdapterVersion,
			Health: healthToProto(provider.Health), UnavailableReason: provider.UnavailableReason,
			Capabilities: &harnessv1.HarnessCapabilities{
				Streaming: provider.Capabilities.Streaming, PersistentSessions: provider.Capabilities.PersistentSessions,
				Resume: provider.Capabilities.Resume, SteerDuringRun: provider.Capabilities.SteerDuringRun,
				Approvals: provider.Capabilities.Approvals, ToolRegistration: provider.Capabilities.ToolRegistration,
				Mcp: provider.Capabilities.MCP, Subagents: provider.Capabilities.Subagents,
				WorkspaceMount: provider.Capabilities.WorkspaceMount, StructuredArtifacts: provider.Capabilities.StructuredArtifacts,
				UsageReporting:  provider.Capabilities.UsageReporting,
				HardTokenBudget: provider.Capabilities.HardTokenBudget, HardRuntimeDeadline: provider.Capabilities.HardRuntimeDeadline,
			},
		})
	}
	return connect.NewResponse(&harnessv1.GetHarnessCatalogResponse{
		Providers: providers, DefaultProviderId: catalog.DefaultProviderID,
	}), nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, errors.New("provider catalog request canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("provider catalog request timed out"))
	case errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("provider catalog is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeUnavailable, errors.New("provider catalog is temporarily unavailable"))
	}
}

func healthToProto(value domain.Health) commonv1.HealthState {
	switch value {
	case domain.HealthStarting:
		return commonv1.HealthState_HEALTH_STATE_STARTING
	case domain.HealthHealthy:
		return commonv1.HealthState_HEALTH_STATE_HEALTHY
	case domain.HealthDegraded:
		return commonv1.HealthState_HEALTH_STATE_DEGRADED
	case domain.HealthUnavailable:
		return commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
	default:
		return commonv1.HealthState_HEALTH_STATE_UNSPECIFIED
	}
}
