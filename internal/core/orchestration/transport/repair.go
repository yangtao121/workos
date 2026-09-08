// Repair task admission (ADR-0016 §5): the private Core RPC the
// reliability-host repair orchestrator calls to turn an incident into an
// ordinary queued Agent task. The task rides the complete standard chain —
// provider resolution from the project binding, credential snapshots,
// queue/lease/terminal machinery, server-side deadlines — and is idempotent
// through the orchestrator's deterministic per-incident key. The payload
// canonically carries the incident reference (`incident_id`), so every
// consumer of the task stream sees the repair linkage. The service is never
// in the Gateway allowlist: only the reliability-host on the private loopback
// reaches it.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type RepairTaskHandler struct {
	admission *orchestration.RepairAdmission
}

func NewRepairTaskHandler(admission *orchestration.RepairAdmission) (string, http.Handler) {
	return agentv1connect.NewAgentRepairTaskServiceHandler(&RepairTaskHandler{admission: admission}, connect.WithReadMaxBytes(4*1024))
}
func (h *RepairTaskHandler) CreateRepairTask(ctx context.Context, req *connect.Request[agentv1.CreateRepairTaskRequest]) (*connect.Response[agentv1.CreateRepairTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.admission.Submit(ctx, orchestration.RepairAdmissionInput{OwnerUserID: id.UserID, ProjectID: req.Msg.GetProjectId(), InstallationID: req.Msg.GetAppInstanceId(), IncidentID: req.Msg.GetIncidentId(), IdempotencyKey: req.Msg.GetIdempotencyKey(), Summary: req.Msg.GetViolationSummary()})
	if err != nil {
		code := connect.CodeInternal
		switch {
		case errors.Is(err, agentdomain.ErrInvalid), errors.Is(err, projectdomain.ErrInvalid):
			code = connect.CodeInvalidArgument
		case errors.Is(err, agentdomain.ErrIdempotencyConflict):
			code = connect.CodeAborted
		case errors.Is(err, agentdomain.ErrProjectDenied):
			code = connect.CodePermissionDenied
		case errors.Is(err, projectdomain.ErrNotFound):
			code = connect.CodeNotFound
		case errors.Is(err, agentdomain.ErrProviderCapabilityMissing), errors.Is(err, agentdomain.ErrProviderUnavailable), errors.Is(err, agentdomain.ErrProviderCredentialMissing):
			code = connect.CodeFailedPrecondition
		case errors.Is(err, agentports.ErrStoreUnavailable), errors.Is(err, projectports.ErrStoreUnavailable):
			code = connect.CodeUnavailable
		}
		return nil, connect.NewError(code, errors.New("repair task admission failed"))
	}
	return connect.NewResponse(&agentv1.CreateRepairTaskResponse{TaskId: result.ID, ProviderId: result.ProviderID, Replay: !result.Created}), nil
}
