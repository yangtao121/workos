package transport

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// maxUsageRequestBytes bounds the decoded usage request before business code
// runs.
const maxUsageRequestBytes = 8 * 1024

// UsageHandler is the public, identity-protected AgentAppUsageService. It
// reports reserved allowance and observed usage as separate facts; cost is
// only echoed when a verified observation exists — absent, never zero.
type UsageHandler struct {
	service *application.UsageService
}

// NewUsageConnectHandler returns the Connect path and handler for the public
// usage service.
func NewUsageConnectHandler(service *application.UsageService) (string, http.Handler) {
	return agentv1connect.NewAgentAppUsageServiceHandler(&UsageHandler{service: service},
		connect.WithReadMaxBytes(maxUsageRequestBytes))
}

func (h *UsageHandler) GetAppUsage(ctx context.Context, req *connect.Request[agentv1.GetAppUsageRequest]) (*connect.Response[agentv1.GetAppUsageResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	policy, usage, err := h.service.AppDailyUsageWithPolicy(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetInstallationId(), req.Msg.GetUtcDate())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.GetAppUsageResponse{Usage: &agentv1.AgentAppDailyUsage{
		InstallationId:       req.Msg.GetInstallationId(),
		UtcDate:              usage.UTCDate,
		TasksReserved:        usage.TasksReserved,
		OutputTokensReserved: usage.OutputTokensReserved,
		TasksRecorded:        usage.TasksRecorded,
		InputTokensRecorded:  usage.InputTokensRecorded,
		OutputTokensRecorded: usage.OutputTokensRecorded,
		CostDecimalRecorded:  usage.CostDecimalRecorded,
		QuotaBreached:        usage.QuotaBreached,
		PolicyRevision:       policy.Revision,
	}}), nil
}
