package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

type SessionToolExecutor interface {
	Execute(context.Context, string, string, string, string, map[string]any) (map[string]any, error)
}
type toolHandler struct{ executor SessionToolExecutor }

func NewToolHandler(executor SessionToolExecutor) (string, http.Handler) {
	return taskexecutionv1connect.NewTaskToolServiceHandler(&toolHandler{executor}, connect.WithReadMaxBytes(MaxExecutionRequestBytes))
}
func (h *toolHandler) ExecuteTaskTool(ctx context.Context, req *connect.Request[taskv1.ExecuteTaskToolRequest]) (*connect.Response[taskv1.ExecuteTaskToolResponse], error) {
	in := req.Msg
	var result map[string]any
	var err error
	if in.DelegationId != "" {
		executor, ok := h.executor.(interface {
			ExecuteDelegated(context.Context, string, string, string, string, string, map[string]any) (map[string]any, error)
		})
		if !ok {
			return nil, connect.NewError(connect.CodeUnimplemented, errors.New("delegation tools unavailable"))
		}
		result, err = executor.ExecuteDelegated(ctx, in.LeaseId, in.WorkerId, in.OperationId, in.Operation, in.DelegationId, in.GetArguments().AsMap())
	} else {
		result, err = h.executor.Execute(ctx, in.LeaseId, in.WorkerId, in.OperationId, in.Operation, in.GetArguments().AsMap())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("tool authorization or execution failed; inspect workspace effects before retrying"))
	}
	value, err := structpb.NewStruct(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("invalid tool result"))
	}
	return connect.NewResponse(&taskv1.ExecuteTaskToolResponse{Result: value}), nil
}
