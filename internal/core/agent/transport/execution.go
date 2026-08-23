package transport

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
)

type ExecutionHandler struct{ service *application.Service }

func NewExecution(service *application.Service) *ExecutionHandler {
	return &ExecutionHandler{service: service}
}

func (h *ExecutionHandler) ClaimTask(ctx context.Context, req *connect.Request[taskv1.ClaimTaskRequest]) (*connect.Response[taskv1.ClaimTaskResponse], error) {
	duration := durationFromProto(req.Msg.GetLeaseDuration())
	lease, err := h.service.Claim(ctx, req.Msg.GetWorkerId(), duration)
	if err != nil {
		return nil, mapError(err)
	}
	response := &taskv1.ClaimTaskResponse{}
	if lease != nil {
		response.Lease = &taskv1.TaskLease{LeaseId: lease.ID, WorkerId: lease.WorkerID, Task: taskToProto(lease.Task), ExpiresAt: timestamppb.New(lease.ExpiresAt)}
	}
	return connect.NewResponse(response), nil
}

func (h *ExecutionHandler) RenewTaskLease(ctx context.Context, req *connect.Request[taskv1.RenewTaskLeaseRequest]) (*connect.Response[taskv1.RenewTaskLeaseResponse], error) {
	expires, cancelled, err := h.service.Renew(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), durationFromProto(req.Msg.GetLeaseDuration()))
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&taskv1.RenewTaskLeaseResponse{ExpiresAt: timestamppb.New(expires), CancellationRequested: cancelled}), nil
}

func (h *ExecutionHandler) AppendTaskEvent(ctx context.Context, req *connect.Request[taskv1.AppendTaskEventRequest]) (*connect.Response[taskv1.AppendTaskEventResponse], error) {
	event := req.Msg.GetEvent()
	if event == nil || event.Event == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	event.Id, event.TaskId, event.Sequence, event.OccurredAt = "", "", 0, nil
	eventType, state, providerID, runID := classifyEvent(event)
	payload, err := protojson.Marshal(event)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	stored, err := h.service.AppendEvent(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), eventType, payload, state, providerID, runID)
	if err != nil {
		return nil, mapError(err)
	}
	var result agentv1.AgentEvent
	if err := protojson.Unmarshal(stored.Payload, &result); err != nil {
		return nil, connect.NewError(connect.CodeDataLoss, errors.New("stored task event is invalid"))
	}
	return connect.NewResponse(&taskv1.AppendTaskEventResponse{StoredEvent: &result}), nil
}

func (h *ExecutionHandler) FinishTaskLease(ctx context.Context, req *connect.Request[taskv1.FinishTaskLeaseRequest]) (*connect.Response[taskv1.FinishTaskLeaseResponse], error) {
	if err := h.service.Finish(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId()); err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&taskv1.FinishTaskLeaseResponse{}), nil
}

func classifyEvent(event *agentv1.AgentEvent) (string, domain.State, string, string) {
	switch value := event.Event.(type) {
	case *agentv1.AgentEvent_RunStarted:
		return "run_started", domain.StateRunning, value.RunStarted.GetProviderId(), value.RunStarted.GetRunId()
	case *agentv1.AgentEvent_RunWaiting:
		return "run_waiting", domain.StateWaiting, "", ""
	case *agentv1.AgentEvent_RunCompleted:
		return "run_completed", domain.StateCompleted, "", ""
	case *agentv1.AgentEvent_RunFailed:
		return "run_failed", domain.StateFailed, "", ""
	case *agentv1.AgentEvent_RunCancelled:
		return "run_cancelled", domain.StateCancelled, "", ""
	case *agentv1.AgentEvent_AssistantDelta:
		return "assistant_delta", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_AssistantMessage:
		return "assistant_message", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ToolCallStarted:
		return "tool_call_started", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ToolCallCompleted:
		return "tool_call_completed", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ApprovalRequired:
		return "approval_required", domain.StateWaiting, "", ""
	case *agentv1.AgentEvent_ArtifactCreated:
		return "artifact_created", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_UsageRecorded:
		return "usage_recorded", domain.StateRunning, "", ""
	default:
		return "unknown", domain.StateRunning, "", ""
	}
}

func durationFromProto(value *durationpb.Duration) time.Duration {
	if value == nil {
		return 0
	}
	return value.AsDuration()
}
