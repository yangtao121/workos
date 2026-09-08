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
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// maxRepairSpecBytes bounds the sanitized violation summary carried as the
// repair task goal.
const maxRepairSpecBytes = 512

// RepairTaskHandler serves the private AgentRepairTaskService on Core.
type RepairTaskHandler struct {
	router *orchestration.TaskRouter
}

// NewRepairTaskHandler wires the private transport. The read limit is small:
// repair RPCs carry bounded facts only (no logs, no payloads, no content).
func NewRepairTaskHandler(router *orchestration.TaskRouter) (string, http.Handler) {
	return agentv1connect.NewAgentRepairTaskServiceHandler(
		&RepairTaskHandler{router: router},
		connect.WithReadMaxBytes(4*1024),
	)
}

func (h *RepairTaskHandler) CreateRepairTask(ctx context.Context, req *connect.Request[agentv1.CreateRepairTaskRequest]) (*connect.Response[agentv1.CreateRepairTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	msg := req.Msg
	spec := strings.TrimSpace(msg.GetViolationSummary())
	switch {
	case id.UserID == "" || id.DeviceID == "":
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("identity is required"))
	case !agentdomain.ValidAppTaskUUID(msg.GetIncidentId()) || !agentdomain.ValidAppTaskUUID(msg.GetProjectId()):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("incident and project must be canonical UUIDv7"))
	case spec == "" || len(spec) > maxRepairSpecBytes || strings.ContainsFunc(spec, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("violation summary is invalid"))
	case msg.GetIdempotencyKey() == "" || len(msg.GetIdempotencyKey()) > 128:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("idempotency key is required"))
	}

	// The repair task payload canonically carries the incident reference so
	// the linkage is visible on the task stream like any other input fact.
	input := &agentv1.AgentTaskInput{
		TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: msg.GetProjectId()}},
		Role:        "general",
		Goal:        spec,
		IncidentId:  msg.GetIncidentId(),
	}
	payload, err := protojson.Marshal(input)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("repair task input is invalid"))
	}

	task, err := h.router.Submit(ctx, agentapp.SubmitInput{
		OwnerUserID:    id.UserID,
		IdempotencyKey: msg.GetIdempotencyKey(),
		ProjectID:      msg.GetProjectId(),
		Payload:        payload,
	})
	if err != nil {
		if errors.Is(err, agentdomain.ErrIdempotencyConflict) {
			return nil, connect.NewError(connect.CodeAborted, agentdomain.ErrIdempotencyConflict)
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("repair task admission failed"))
	}
	return connect.NewResponse(&agentv1.CreateRepairTaskResponse{
		TaskId:     task.ID,
		ProviderId: task.ProviderID,
		Replay:     task.State != agentdomain.StateQueued,
	}), nil
}
