package transport

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Submitter interface {
	Submit(context.Context, application.SubmitInput) (domain.Task, error)
}

type Handler struct {
	service   *application.Service
	submitter Submitter
}

func New(service *application.Service, submitter Submitter) *Handler {
	return &Handler{service: service, submitter: submitter}
}

func (h *Handler) SubmitTask(ctx context.Context, req *connect.Request[agentv1.SubmitTaskRequest]) (*connect.Response[agentv1.SubmitTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	input := req.Msg.GetInput()
	if input == nil || input.GetTargetScope() == nil || strings.TrimSpace(input.GetGoal()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	projectID := ""
	switch scope := input.TargetScope.Scope.(type) {
	case *agentv1.TargetScope_ProjectId:
		projectID = scope.ProjectId
	case *agentv1.TargetScope_Global:
		if !scope.Global {
			return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	payload, err := protojson.Marshal(input)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	task, err := h.submitter.Submit(ctx, application.SubmitInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(), ProjectID: projectID, Payload: payload,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.SubmitTaskResponse{Task: taskToProto(task)}), nil
}

func (h *Handler) GetTask(ctx context.Context, req *connect.Request[agentv1.GetTaskRequest]) (*connect.Response[agentv1.GetTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	task, err := h.service.Get(ctx, id.UserID, req.Msg.GetTaskId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.GetTaskResponse{Task: taskToProto(task)}), nil
}

func (h *Handler) ListTasks(ctx context.Context, req *connect.Request[agentv1.ListTasksRequest]) (*connect.Response[agentv1.ListTasksResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	limit, cursor := 0, ""
	if req.Msg.Page != nil {
		limit, cursor = int(req.Msg.Page.PageSize), req.Msg.Page.PageToken
	}
	tasks, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), cursor, limit)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*agentv1.AgentTask, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, taskToProto(task))
	}
	next := ""
	if limit > 0 && len(items) == limit {
		next = items[len(items)-1].GetId()
	}
	return connect.NewResponse(&agentv1.ListTasksResponse{Tasks: items, Page: &commonv1.PageResponse{NextPageToken: next}}), nil
}

func (h *Handler) CancelTask(ctx context.Context, req *connect.Request[agentv1.CancelTaskRequest]) (*connect.Response[agentv1.CancelTaskResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	task, _, err := h.service.Cancel(ctx, id.UserID, req.Msg.GetTaskId(), req.Msg.GetReason())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&agentv1.CancelTaskResponse{Task: taskToProto(task)}), nil
}

func (h *Handler) WatchTaskEvents(ctx context.Context, req *connect.Request[agentv1.WatchTaskEventsRequest], stream *connect.ServerStream[agentv1.WatchTaskEventsResponse]) error {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	after := req.Msg.GetAfterSequence()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, listErr := h.service.Events(ctx, id.UserID, req.Msg.GetTaskId(), after, 100)
		if listErr != nil {
			return mapError(listErr)
		}
		for _, stored := range events {
			var event agentv1.AgentEvent
			if err := protojson.Unmarshal(stored.Payload, &event); err != nil {
				return connect.NewError(connect.CodeDataLoss, errors.New("stored task event is invalid"))
			}
			if err := stream.Send(&agentv1.WatchTaskEventsResponse{Event: &event}); err != nil {
				return err
			}
			after = stored.Sequence
		}
		task, getErr := h.service.Get(ctx, id.UserID, req.Msg.GetTaskId())
		if getErr != nil {
			return mapError(getErr)
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

func taskToProto(task domain.Task) *agentv1.AgentTask {
	input := &agentv1.AgentTaskInput{}
	_ = protojson.Unmarshal(task.Input, input)
	return &agentv1.AgentTask{
		Id: task.ID, OwnerUserId: task.OwnerUserID, Input: input, State: stateToProto(task.State),
		ProviderId: task.ProviderID, HarnessInstanceId: task.HarnessInstanceID, RunId: task.RunID,
		LastEventSequence: task.LastEventSequence, CreatedAt: timestamppb.New(task.CreatedAt), UpdatedAt: timestamppb.New(task.UpdatedAt),
	}
}

func stateToProto(state domain.State) agentv1.AgentTaskState {
	return map[domain.State]agentv1.AgentTaskState{
		domain.StateQueued:    agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED,
		domain.StateRunning:   agentv1.AgentTaskState_AGENT_TASK_STATE_RUNNING,
		domain.StateWaiting:   agentv1.AgentTaskState_AGENT_TASK_STATE_WAITING,
		domain.StateCompleted: agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED,
		domain.StateFailed:    agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED,
		domain.StateCancelled: agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED,
	}[state]
}

func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrProjectDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrLeaseLost):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, domain.ErrTerminal):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, domain.ErrProviderMismatch):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("agent task operation failed"))
	}
}
