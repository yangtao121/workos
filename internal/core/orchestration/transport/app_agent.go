package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// AppAgentRunner is the orchestration service surface the handler needs; the
// composition root passes the concrete service, tests pass fakes.
type AppAgentRunner interface {
	RunAgentTask(ctx context.Context, ownerUserID, projectID, appInstanceID, clientKey, role, goal string) (agentdomain.Task, error)
	WatchAgentTaskEvents(ctx context.Context, ownerUserID, projectID, appInstanceID, taskID string, after int64, limit int) (agentdomain.Task, []agentdomain.Event, error)
}

// AppAgentHandler exposes the private Core App Agent service. It is never on
// the gateway allowlist: only runtime-host reaches it with forwarded trusted
// identity, and every request re-validates installation and grant in the
// orchestration service.
type AppAgentHandler struct {
	service AppAgentRunner
}

func NewAppAgent(service AppAgentRunner) *AppAgentHandler {
	return &AppAgentHandler{service: service}
}

// NewAppAgentConnectHandler wires the private App Agent transport. The read
// limit bounds the decoded request: the bounded run/watch inputs stay far
// below it even with base64 inflation.
func NewAppAgentConnectHandler(service AppAgentRunner) (string, http.Handler) {
	return agentv1connect.NewAppAgentServiceHandler(
		NewAppAgent(service),
		connect.WithReadMaxBytes(32*1024),
	)
}

func (h *AppAgentHandler) RunAgentTask(ctx context.Context, req *connect.Request[agentv1.RunAgentTaskRequest]) (*connect.Response[agentv1.RunAgentTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	task, err := h.service.RunAgentTask(
		ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetAppInstanceId(),
		req.Msg.GetClientIdempotencyKey(), req.Msg.GetRole(), req.Msg.GetGoal(),
	)
	if err != nil {
		return nil, mapAppAgentError(err)
	}
	return connect.NewResponse(&agentv1.RunAgentTaskResponse{
		TaskId: task.ID, State: appAgentStateToProto(task.State), LastEventSequence: task.LastEventSequence,
	}), nil
}

func (h *AppAgentHandler) WatchAgentTaskEvents(ctx context.Context, req *connect.Request[agentv1.WatchAgentTaskEventsRequest], stream *connect.ServerStream[agentv1.WatchAgentTaskEventsResponse]) error {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	after := req.Msg.GetAfterSequence()
	if after < 0 {
		return connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, events, listErr := h.service.WatchAgentTaskEvents(
			ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetAppInstanceId(),
			req.Msg.GetTaskId(), after, 100,
		)
		if listErr != nil {
			return mapAppAgentError(listErr)
		}
		for _, stored := range events {
			var event agentv1.AgentEvent
			if err := protojson.Unmarshal(stored.Payload, &event); err != nil {
				return connect.NewError(connect.CodeDataLoss, errors.New("stored task event is invalid"))
			}
			if err := stream.Send(&agentv1.WatchAgentTaskEventsResponse{Event: &event}); err != nil {
				return err
			}
			after = stored.Sequence
		}
		if task.State.Terminal() && after >= task.LastEventSequence {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// appAgentStateToProto maps the Agent task state to the canonical enum.
func appAgentStateToProto(state domain.State) agentv1.AgentTaskState {
	return map[domain.State]agentv1.AgentTaskState{
		domain.StateQueued:    agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED,
		domain.StateRunning:   agentv1.AgentTaskState_AGENT_TASK_STATE_RUNNING,
		domain.StateWaiting:   agentv1.AgentTaskState_AGENT_TASK_STATE_WAITING,
		domain.StateCompleted: agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED,
		domain.StateFailed:    agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED,
		domain.StateCancelled: agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED,
	}[state]
}

// mapAppAgentError converts App Agent verdicts to sanitized Connect codes.
// Foreign/unknown/archived/uninstalled resources are indistinguishable
// misses; grant denials are PermissionDenied; stored-grant corruption falls
// through to the sanitized Internal default; store outages stay retryable
// Unavailable.
func mapAppAgentError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, projectdomain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("app agent request is invalid"))
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, projectdomain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("app task is not available"))
	case errors.Is(err, domain.ErrProjectDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("app project is not available"))
	case errors.Is(err, orchestration.ErrAppNotGranted):
		return connect.NewError(connect.CodePermissionDenied, errors.New("app capability is not granted"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, projectports.ErrStoreUnavailable), errors.Is(err, agentports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("app agent service is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("app agent operation failed"))
	}
}
