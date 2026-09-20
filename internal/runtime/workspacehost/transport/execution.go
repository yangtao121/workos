package transport

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"google.golang.org/protobuf/types/known/structpb"
	"net/http"
)

type Executor interface {
	Execute(context.Context, domain.Operation) (domain.Result, error)
}
type executionHandler struct{ service Executor }

func NewExecutionHandler(service Executor) (string, http.Handler) {
	return workloadv1connect.NewWorkspaceExecutionServiceHandler(&executionHandler{service}, connect.WithReadMaxBytes(768*1024))
}
func (h *executionHandler) ExecuteWorkspaceOperation(ctx context.Context, req *connect.Request[workloadv1.ExecuteWorkspaceOperationRequest]) (*connect.Response[workloadv1.ExecuteWorkspaceOperationResponse], error) {
	in := req.Msg
	if in.DelegationId != "" || in.ParentTaskId != "" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("delegated worktrees unavailable"))
	}
	result, err := h.service.Execute(ctx, domain.Operation{BindingID: in.WorkspaceBindingId, Revision: in.WorkspaceRevision, ID: in.OperationId, OwnerUserID: in.OwnerUserId, ProjectID: in.ProjectId, SourceID: in.WorkspaceSourceId, ReadOnly: in.ReadOnly, Name: in.Operation, Arguments: in.GetArguments().AsMap()})
	if err != nil {
		code := connect.CodeUnavailable
		switch {
		case errors.Is(err, domain.ErrDenied):
			code = connect.CodePermissionDenied
		case errors.Is(err, domain.ErrInvalid):
			code = connect.CodeInvalidArgument
		case errors.Is(err, domain.ErrUnknownOutcome):
			code = connect.CodeFailedPrecondition
		}
		return nil, connect.NewError(code, errors.New("workspace operation failed; inspect effects before retrying"))
	}
	value, err := structpb.NewStruct(map[string]any(result))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("workspace result invalid"))
	}
	return connect.NewResponse(&workloadv1.ExecuteWorkspaceOperationResponse{Result: value}), nil
}
