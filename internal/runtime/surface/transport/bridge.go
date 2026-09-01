// The public App Bridge transport. Request bodies carry only bounded app
// input; owner and device come from the trusted gateway identity, the
// ephemeral bridge token from dedicated metadata, and project/app-instance
// from the server-side session the token resolves.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	bridgev1connect "github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// App bridge bounded-input grammar mirrors the Core contract exactly: the
// same limits are enforced here so malformed input fails closed at the edge.
const (
	MaxBridgeIdempotencyKeyRunes = 128
	MaxBridgeRoleRunes           = 64
	MaxBridgeGoalBytes           = 16 * 1024
)

// BridgeService abstracts the application bridge use cases for tests.
type BridgeService interface {
	RunAgentTask(ctx context.Context, ownerUserID, deviceID, token, idempotencyKey, role, goal string) (ports.AppTaskSubmission, error)
	StreamAgentEvents(ctx context.Context, ownerUserID, deviceID, token, taskID string, after int64, onEvent func(*agentv1.AgentEvent) error) error
}

type BridgeHandler struct {
	service BridgeService
}

func NewBridge(service BridgeService) *BridgeHandler {
	return &BridgeHandler{service: service}
}

// NewBridgeConnectHandler wires the public App Bridge transport. The read
// limit bounds the decoded request (goal ≤ 16 KiB with headroom), so an
// oversized or compressed-payload request is rejected before any validation
// runs.
func NewBridgeConnectHandler(service BridgeService) (string, http.Handler) {
	return bridgev1connect.NewAppBridgeServiceHandler(
		NewBridge(service),
		connect.WithReadMaxBytes(32*1024),
	)
}

func (h *BridgeHandler) RunAgentTask(ctx context.Context, req *connect.Request[bridgev1.RunAgentTaskRequest]) (*connect.Response[bridgev1.RunAgentTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	token := req.Header().Get(identity.BridgeTokenHeader)
	if !validBridgeRunInput(req.Msg.GetIdempotencyKey(), req.Msg.GetRole(), req.Msg.GetGoal()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bridge request is invalid"))
	}
	submission, err := h.service.RunAgentTask(ctx, id.UserID, id.DeviceID, token,
		req.Msg.GetIdempotencyKey(), req.Msg.GetRole(), req.Msg.GetGoal())
	if err != nil {
		return nil, mapBridgeError(err)
	}
	return connect.NewResponse(&bridgev1.RunAgentTaskResponse{
		TaskId: submission.TaskID, State: bridgeStateToProto(submission.State), LastEventSequence: submission.LastEventSequence,
	}), nil
}

func (h *BridgeHandler) WatchAgentTaskEvents(ctx context.Context, req *connect.Request[bridgev1.WatchAgentTaskEventsRequest], stream *connect.ServerStream[bridgev1.WatchAgentTaskEventsResponse]) error {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	token := req.Header().Get(identity.BridgeTokenHeader)
	after := req.Msg.GetAfterSequence()
	if after < 0 || !validBridgeTaskID(req.Msg.GetTaskId()) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("bridge request is invalid"))
	}
	err = h.service.StreamAgentEvents(ctx, id.UserID, id.DeviceID, token, req.Msg.GetTaskId(), after, func(event *agentv1.AgentEvent) error {
		return stream.Send(&bridgev1.WatchAgentTaskEventsResponse{Event: event})
	})
	if err != nil {
		return mapBridgeError(err)
	}
	return nil
}

// SearchKnowledge is the honest pre-implementation verdict (ADR-0013): the
// protocol surface exists, but no working executor is wired yet, so every
// call receives an explicit Unimplemented. `knowledge.read` grants never
// enter a session's effective capabilities while this is the case.
func (h *BridgeHandler) SearchKnowledge(ctx context.Context, req *connect.Request[bridgev1.SearchKnowledgeRequest]) (*connect.Response[bridgev1.SearchKnowledgeResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("bridge method is not implemented"))
}

func validBridgeRunInput(idempotencyKey, role, goal string) bool {
	return len(idempotencyKey) >= 1 && len([]rune(idempotencyKey)) <= MaxBridgeIdempotencyKeyRunes &&
		len([]rune(role)) <= MaxBridgeRoleRunes &&
		len(goal) >= 1 && len(goal) <= MaxBridgeGoalBytes
}

func validBridgeTaskID(taskID string) bool {
	return len(taskID) == 36
}

func bridgeStateToProto(state string) agentv1.AgentTaskState {
	value, ok := agentv1.AgentTaskState_value[state]
	if !ok {
		return agentv1.AgentTaskState_AGENT_TASK_STATE_UNSPECIFIED
	}
	return agentv1.AgentTaskState(value)
}

// mapBridgeError converts bridge verdicts to sanitized Connect codes with
// fixed short messages: no token fragments, no Core internals, no goal or
// event content, no foreign-resource existence detail. Credential failures
// are always Unauthenticated; capability denials are PermissionDenied.
func mapBridgeError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("bridge request is invalid"))
	case errors.Is(err, domain.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("bridge credential is not valid"))
	case errors.Is(err, domain.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("bridge capability is not granted"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("app task is not available"))
	case errors.Is(err, ports.ErrAppAgentConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, ports.ErrAppAgentExhausted):
		return connect.NewError(connect.CodeResourceExhausted, errors.New("app task allowance is exhausted"))
	case errors.Is(err, ports.ErrAppAgentDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("bridge capability is not granted"))
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, ports.ErrStoreUnavailable), errors.Is(err, ports.ErrAppAgentUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("bridge is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("bridge operation failed"))
	}
}
