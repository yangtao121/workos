package transport

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"connectrpc.com/connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type WorkspaceHandler struct {
	service *application.WorkspaceService
}

func NewWorkspaceHandler(service *application.WorkspaceService) (string, http.Handler) {
	return projectv1connect.NewProjectWorkspaceServiceHandler(&WorkspaceHandler{service: service})
}

func workspaceBindingProto(binding domain.WorkspaceBinding) *projectv1.WorkspaceBinding {
	state := projectv1.WorkspaceBindingState(projectv1.WorkspaceBindingState_value["WORKSPACE_BINDING_STATE_"+strings.ToUpper(string(binding.State))])
	out := &projectv1.WorkspaceBinding{
		Id: binding.ID, OwnerUserId: binding.OwnerUserID, ProjectId: binding.ProjectID,
		WorkspaceSourceId: binding.WorkspaceSourceID, DisplayName: binding.DisplayName,
		ReadOnly: binding.ReadOnly, State: state, Revision: binding.Revision,
		CreatedAt: timestamppb.New(binding.CreatedAt), UpdatedAt: timestamppb.New(binding.UpdatedAt),
	}
	if binding.ArchivedAt != nil {
		out.ArchivedAt = timestamppb.New(*binding.ArchivedAt)
	}
	return out
}

func (h *WorkspaceHandler) BindWorkspace(ctx context.Context, req *connect.Request[projectv1.BindWorkspaceRequest]) (*connect.Response[projectv1.BindWorkspaceResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	binding, err := h.service.Bind(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetWorkspaceSourceId(), req.Msg.GetDisplayName(), req.Msg.GetIdempotencyKey())
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&projectv1.BindWorkspaceResponse{Binding: workspaceBindingProto(binding)}), nil
}

func (h *WorkspaceHandler) GetWorkspace(ctx context.Context, req *connect.Request[projectv1.GetWorkspaceRequest]) (*connect.Response[projectv1.GetWorkspaceResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	binding, err := h.service.Get(ctx, owner.UserID, req.Msg.GetBindingId())
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&projectv1.GetWorkspaceResponse{Binding: workspaceBindingProto(binding)}), nil
}

func (h *WorkspaceHandler) ListProjectWorkspaces(ctx context.Context, req *connect.Request[projectv1.ListProjectWorkspacesRequest]) (*connect.Response[projectv1.ListProjectWorkspacesResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	bindings, err := h.service.List(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, workspaceError(err)
	}
	out := make([]*projectv1.WorkspaceBinding, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, workspaceBindingProto(binding))
	}
	return connect.NewResponse(&projectv1.ListProjectWorkspacesResponse{Bindings: out}), nil
}

func (h *WorkspaceHandler) UpdateWorkspaceAccess(ctx context.Context, req *connect.Request[projectv1.UpdateWorkspaceAccessRequest]) (*connect.Response[projectv1.UpdateWorkspaceAccessResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	binding, err := h.service.UpdateAccess(ctx, owner.UserID, req.Msg.GetBindingId(), req.Msg.GetReadOnly(), req.Msg.GetExpectedRevision())
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&projectv1.UpdateWorkspaceAccessResponse{Binding: workspaceBindingProto(binding)}), nil
}

func (h *WorkspaceHandler) ArchiveWorkspace(ctx context.Context, req *connect.Request[projectv1.ArchiveWorkspaceRequest]) (*connect.Response[projectv1.ArchiveWorkspaceResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	binding, err := h.service.Archive(ctx, owner.UserID, req.Msg.GetBindingId(), req.Msg.GetExpectedRevision())
	if err != nil {
		return nil, workspaceError(err)
	}
	return connect.NewResponse(&projectv1.ArchiveWorkspaceResponse{Binding: workspaceBindingProto(binding)}), nil
}

func workspaceError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrWorkspaceSourceUnknown):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrWorkspaceRevision):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrWorkspaceConflict):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrWorkspaceActiveExists):
		code = connect.CodeFailedPrecondition
	}
	return connect.NewError(code, errors.New("project workspace request failed"))
}

func (h *WorkspaceHandler) ListAvailableWorkspaces(ctx context.Context, req *connect.Request[projectv1.ListAvailableWorkspacesRequest]) (*connect.Response[projectv1.ListAvailableWorkspacesResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	sources, err := h.service.AvailableForProject(ctx, owner.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, workspaceError(err)
	}
	result := make([]*projectv1.AvailableWorkspaceSource, 0, len(sources))
	for _, source := range sources {
		result = append(result, &projectv1.AvailableWorkspaceSource{Id: source.ID, DisplayName: source.DisplayName, Kind: source.Kind, ReadOnly: source.ReadOnly})
	}
	return connect.NewResponse(&projectv1.ListAvailableWorkspacesResponse{Sources: result}), nil
}

func (h *WorkspaceHandler) ListWorkspaceFiles(ctx context.Context, req *connect.Request[projectv1.ListWorkspaceFilesRequest]) (*connect.Response[projectv1.ListWorkspaceFilesResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, binding, err := h.service.ExecuteFile(ctx, owner.UserID, req.Msg.GetProjectId(), "fs.list", req.Msg.GetPath(), "", "", 0)
	if err != nil {
		return nil, workspaceError(err)
	}
	entries := []*projectv1.WorkspaceFileEntry{}
	rows, _ := result["entries"].([]any)
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		kind, _ := entry["type"].(string)
		size, _ := entry["size"].(float64)
		entries = append(entries, &projectv1.WorkspaceFileEntry{Path: path.Join(req.Msg.GetPath(), name), Kind: kind, Size: int64(size)})
	}
	return connect.NewResponse(&projectv1.ListWorkspaceFilesResponse{Entries: entries, WorkspaceRevision: binding.Revision, ReadOnly: binding.ReadOnly}), nil
}
func (h *WorkspaceHandler) ReadWorkspaceFile(ctx context.Context, req *connect.Request[projectv1.ReadWorkspaceFileRequest]) (*connect.Response[projectv1.ReadWorkspaceFileResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, binding, err := h.service.ExecuteFile(ctx, owner.UserID, req.Msg.GetProjectId(), "fs.read", req.Msg.GetPath(), "", "", 0)
	if err != nil {
		return nil, workspaceError(err)
	}
	content, _ := result["content"].(string)
	etag, _ := result["version"].(string)
	return connect.NewResponse(&projectv1.ReadWorkspaceFileResponse{Content: content, Etag: etag, WorkspaceRevision: binding.Revision, ReadOnly: binding.ReadOnly}), nil
}
func (h *WorkspaceHandler) WriteWorkspaceFile(ctx context.Context, req *connect.Request[projectv1.WriteWorkspaceFileRequest]) (*connect.Response[projectv1.WriteWorkspaceFileResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, _, err := h.service.ExecuteFile(ctx, owner.UserID, req.Msg.GetProjectId(), "fs.write", req.Msg.GetPath(), req.Msg.GetContent(), req.Msg.GetExpectedEtag(), req.Msg.GetWorkspaceRevision())
	if err != nil {
		return nil, workspaceError(err)
	}
	etag, _ := result["version"].(string)
	return connect.NewResponse(&projectv1.WriteWorkspaceFileResponse{Etag: etag}), nil
}
