package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

func (h *Handler) CreateProject(ctx context.Context, req *connect.Request[projectv1.CreateProjectRequest]) (*connect.Response[projectv1.CreateProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	project, err := h.service.Create(ctx, application.CreateInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(), Name: req.Msg.GetName(),
		Icon: req.Msg.GetIcon(), WorkspaceRefs: workspaceFromProto(req.Msg.GetWorkspaceRefs()),
		HarnessBinding: bindingFromProto(req.Msg.GetHarnessBinding()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.CreateProjectResponse{Project: projectToProto(project)}), nil
}

func (h *Handler) GetProject(ctx context.Context, req *connect.Request[projectv1.GetProjectRequest]) (*connect.Response[projectv1.GetProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	project, err := h.service.Get(ctx, id.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.GetProjectResponse{Project: projectToProto(project)}), nil
}

func (h *Handler) ListProjects(ctx context.Context, req *connect.Request[projectv1.ListProjectsRequest]) (*connect.Response[projectv1.ListProjectsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize, pageToken := 0, ""
	if req.Msg.Page != nil {
		pageSize, pageToken = int(req.Msg.Page.PageSize), req.Msg.Page.PageToken
	}
	projects, err := h.service.List(ctx, id.UserID, pageToken, pageSize, req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*projectv1.Project, 0, len(projects))
	for _, project := range projects {
		items = append(items, projectToProto(project))
	}
	next := ""
	if pageSize > 0 && len(items) == pageSize {
		next = items[len(items)-1].GetId()
	}
	return connect.NewResponse(&projectv1.ListProjectsResponse{Projects: items, Page: (commonPage{NextPageToken: next}).proto()}), nil
}

func (h *Handler) UpdateProject(ctx context.Context, req *connect.Request[projectv1.UpdateProjectRequest]) (*connect.Response[projectv1.UpdateProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	project, err := h.service.Update(ctx, application.UpdateInput{
		OwnerUserID: id.UserID, ProjectID: req.Msg.GetProjectId(), ExpectedRevision: req.Msg.GetExpectedRevision(),
		Name: req.Msg.Name, Icon: req.Msg.Icon, WorkspaceRefs: workspaceFromProto(req.Msg.GetWorkspaceRefs()),
		ReplaceWorkspaceRefs: req.Msg.GetReplaceWorkspaceRefs(), HarnessBinding: bindingFromProto(req.Msg.GetHarnessBinding()),
		ClearHarnessBinding: req.Msg.GetClearHarnessBinding(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.UpdateProjectResponse{Project: projectToProto(project)}), nil
}

func (h *Handler) ArchiveProject(ctx context.Context, req *connect.Request[projectv1.ArchiveProjectRequest]) (*connect.Response[projectv1.ArchiveProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	project, err := h.service.Archive(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetExpectedRevision())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.ArchiveProjectResponse{Project: projectToProto(project)}), nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("project operation failed"))
	}
}

func projectToProto(project domain.Project) *projectv1.Project {
	result := &projectv1.Project{
		Id: project.ID, OwnerUserId: project.OwnerUserID, Name: project.Name, Icon: project.Icon,
		WorkspaceRefs: workspaceToProto(project.WorkspaceRefs), HarnessBinding: bindingToProto(project.HarnessBinding),
		InstalledAppIds: project.InstalledAppIDs, DefaultAgentRole: project.DefaultAgentRole,
		KnowledgeCollectionId: project.KnowledgeCollectionID, ArtifactCollectionId: project.ArtifactCollectionID,
		Revision: project.Revision, CreatedAt: timestamppb.New(project.CreatedAt), UpdatedAt: timestamppb.New(project.UpdatedAt),
	}
	if project.ArchivedAt != nil {
		result.ArchivedAt = timestamppb.New(*project.ArchivedAt)
	}
	return result
}

func workspaceFromProto(items []*projectv1.WorkspaceRef) []domain.WorkspaceRef {
	result := make([]domain.WorkspaceRef, 0, len(items))
	for _, item := range items {
		result = append(result, domain.WorkspaceRef{ID: item.GetId(), Kind: item.GetKind().String(), URI: item.GetUri(), LogicalMount: item.GetLogicalMount(), ReadOnly: item.GetReadOnly()})
	}
	return result
}

func workspaceToProto(items []domain.WorkspaceRef) []*projectv1.WorkspaceRef {
	result := make([]*projectv1.WorkspaceRef, 0, len(items))
	for _, item := range items {
		kind := projectv1.WorkspaceKind_WORKSPACE_KIND_UNSPECIFIED
		if value, ok := projectv1.WorkspaceKind_value[item.Kind]; ok {
			kind = projectv1.WorkspaceKind(value)
		}
		result = append(result, &projectv1.WorkspaceRef{Id: item.ID, Kind: kind, Uri: item.URI, LogicalMount: item.LogicalMount, ReadOnly: item.ReadOnly})
	}
	return result
}

func bindingFromProto(item *projectv1.HarnessBinding) *domain.HarnessBinding {
	if item == nil {
		return nil
	}
	policy := map[projectv1.HarnessInstancePolicy]string{
		projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_PERSISTENT: "persistent",
		projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_LAZY:       "lazy",
		projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL:  "ephemeral",
	}[item.GetInstancePolicy()]
	return &domain.HarnessBinding{ProviderID: item.GetProviderId(), InstancePolicy: policy, ProfileID: item.GetProfileId(), CredentialRef: item.GetCredentialRef(), ResourcePolicyID: item.GetResourcePolicyId()}
}

func bindingToProto(item *domain.HarnessBinding) *projectv1.HarnessBinding {
	if item == nil {
		return nil
	}
	policy := map[string]projectv1.HarnessInstancePolicy{
		"persistent": projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_PERSISTENT,
		"lazy":       projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_LAZY,
		"ephemeral":  projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL,
	}[item.InstancePolicy]
	return &projectv1.HarnessBinding{ProviderId: item.ProviderID, InstancePolicy: policy, ProfileId: item.ProfileID, CredentialRef: item.CredentialRef, ResourcePolicyId: item.ResourcePolicyID}
}

type commonPage struct{ NextPageToken string }
