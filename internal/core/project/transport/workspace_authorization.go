package transport

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/core/project/application"
	"net/http"
)

type workspaceAuthorization struct {
	projects   *application.Service
	workspaces *application.WorkspaceService
}

// NewWorkspaceAuthorizationHandler is private to the process network. Gateway
// must never expose this server-derived identity contract to clients.
func NewWorkspaceAuthorizationHandler(projects *application.Service, workspaces *application.WorkspaceService) (string, http.Handler) {
	return projectv1connect.NewWorkspaceExecutionAuthorizationServiceHandler(&workspaceAuthorization{projects, workspaces})
}
func (h *workspaceAuthorization) ResolveWorkspaceExecution(ctx context.Context, req *connect.Request[projectv1.ResolveWorkspaceExecutionRequest]) (*connect.Response[projectv1.ResolveWorkspaceExecutionResponse], error) {
	owner, project := req.Msg.GetOwnerUserId(), req.Msg.GetProjectId()
	facts, err := h.projects.Get(ctx, owner, project)
	if err != nil || facts.ArchivedAt != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("project execution denied"))
	}
	binding, err := h.workspaces.ActiveForProject(ctx, owner, project)
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&projectv1.ResolveWorkspaceExecutionResponse{Binding: workspaceBindingProto(binding)}), nil
}
