package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/broker"
)

type Handler struct{ broker *broker.Broker }

func New(value *broker.Broker) *Handler { return &Handler{broker: value} }

func (h *Handler) DescribeProviders(context.Context, *connect.Request[harnessv1.DescribeProvidersRequest]) (*connect.Response[harnessv1.DescribeProvidersResponse], error) {
	return connect.NewResponse(&harnessv1.DescribeProvidersResponse{Providers: h.broker.Describe()}), nil
}

func (h *Handler) ExecuteTask(ctx context.Context, req *connect.Request[harnessv1.ExecuteTaskRequest], stream *connect.ServerStream[harnessv1.ExecuteTaskResponse]) error {
	if req.Msg.GetTaskId() == "" || req.Msg.GetInput() == nil || req.Msg.GetProviderId() == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("task, input, and provider are required"))
	}
	// The HarnessHostService streaming surface carries events only; it has
	// no lease-bound artifact sink, so structured artifact output is not
	// materializable through it (ADR-0008). The fake adapter refuses such
	// requests on this path.
	err := h.broker.Run(ctx, req.Msg.GetTaskId(), req.Msg.GetProviderId(), req.Msg.GetInput(), func(event *agentv1.AgentEvent) error {
		return stream.Send(&harnessv1.ExecuteTaskResponse{Event: event})
	}, nil)
	if errors.Is(err, broker.ErrProviderUnavailable) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return err
}

func (h *Handler) CancelRun(_ context.Context, req *connect.Request[harnessv1.CancelRunRequest]) (*connect.Response[harnessv1.CancelRunResponse], error) {
	return connect.NewResponse(&harnessv1.CancelRunResponse{Accepted: h.broker.Cancel(req.Msg.GetTaskId())}), nil
}
