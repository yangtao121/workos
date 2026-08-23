package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projecttransport "github.com/yangtao121/workos/internal/core/project/transport"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Handler struct {
	binder *orchestration.ProjectHarnessBinder
}

func New(binder *orchestration.ProjectHarnessBinder) *Handler { return &Handler{binder: binder} }

func (h *Handler) SetProjectHarnessBinding(ctx context.Context, req *connect.Request[projectv1.SetProjectHarnessBindingRequest]) (*connect.Response[projectv1.SetProjectHarnessBindingResponse], error) {
	identityContext, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	input := orchestration.SetProjectHarnessBindingInput{
		OwnerUserID: identityContext.UserID, ProjectID: req.Msg.GetProjectId(), ExpectedRevision: req.Msg.GetExpectedRevision(),
	}
	switch selection := req.Msg.GetSelection().(type) {
	case *projectv1.SetProjectHarnessBindingRequest_ProviderId:
		input.ProviderID = selection.ProviderId
	case *projectv1.SetProjectHarnessBindingRequest_UseGlobalDefault:
		input.UseGlobalDefault = selection.UseGlobalDefault
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("harness selection is required"))
	}
	project, err := h.binder.Set(ctx, input)
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.SetProjectHarnessBindingResponse{Project: projecttransport.ProjectToProto(project)}), nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, errors.New("project harness update canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("provider catalog request timed out"))
	case errors.Is(err, projectdomain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid project harness selection"))
	case errors.Is(err, projectdomain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("project not found"))
	case errors.Is(err, projectdomain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("project revision conflict"))
	case errors.Is(err, orchestration.ErrProviderUnknown):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("selected provider is not in the catalog"))
	case errors.Is(err, orchestration.ErrProviderNotSelectable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("selected provider is not available for new bindings"))
	case errors.Is(err, catalogdomain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("provider catalog is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("project harness update failed"))
	}
}
