package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	projectv1connect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

// MaxProjectRequestBytes bounds every ProjectService request message before the
// Connect stack decodes it. It is derived from the domain field limits, not
// copied from another service: 16 workspace refs (MaxWorkspaceRefs) of at
// most (4096-byte URI + 512-byte ref ID + 512-byte logical mount + kind/
// JSON overhead ≈ 128 B) reach ~84 KiB in the inflated protojson form; name
// (480 B), icon (512 B), idempotency key (512 B), and the harness binding
// (≤ ~1 KiB) plus framing add a few KiB more — every legal request stays
// under ~90 KiB. 128 KiB (131,072 bytes) holds that with headroom while
// staying a small explicit constant — the library default is unlimited. The
// limit applies per decompressed request message, so gzip bombs are rejected
// on their decoded size; the application-level field checks stay in place,
// so the wire budget only guards decode-time memory. Other handlers on the
// same mux keep their own independent limits.
const MaxProjectRequestBytes = 128 * 1024

// NewProjectConnectHandler wires the transport into a real Connect handler
// with the bounded-read configuration. Composition roots and tests must use
// this constructor so the read limit is identical in production and tests;
// oversize bodies are rejected with ResourceExhausted before any business
// code runs.
func NewProjectConnectHandler(service *application.Service) (string, http.Handler) {
	return projectv1connect.NewProjectServiceHandler(
		New(service),
		connect.WithReadMaxBytes(MaxProjectRequestBytes),
	)
}

func (h *Handler) CreateProject(ctx context.Context, req *connect.Request[projectv1.CreateProjectRequest]) (*connect.Response[projectv1.CreateProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	project, err := h.service.Create(ctx, application.CreateInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(), Name: req.Msg.GetName(),
		Icon: req.Msg.GetIcon(), WorkspaceRefs: workspaceFromProto(req.Msg.GetWorkspaceRefs()),
		HarnessBinding: bindingFromProto(req.Msg.GetHarnessBinding()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.CreateProjectResponse{Project: ProjectToProto(project)}), nil
}

func (h *Handler) GetProject(ctx context.Context, req *connect.Request[projectv1.GetProjectRequest]) (*connect.Response[projectv1.GetProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	project, err := h.service.Get(ctx, id.UserID, req.Msg.GetProjectId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.GetProjectResponse{Project: ProjectToProto(project)}), nil
}

func (h *Handler) ListProjects(ctx context.Context, req *connect.Request[projectv1.ListProjectsRequest]) (*connect.Response[projectv1.ListProjectsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	pageSize, pageToken := 0, ""
	if req.Msg.Page != nil {
		pageSize, pageToken = int(req.Msg.Page.PageSize), req.Msg.Page.PageToken
	}
	// The application owns the effective page size and the next-page probe;
	// transport forwards its token verbatim instead of guessing from the
	// raw request size.
	page, err := h.service.ListProjects(ctx, id.UserID, pageToken, pageSize, req.Msg.GetIncludeArchived())
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*projectv1.Project, 0, len(page.Items))
	for _, project := range page.Items {
		items = append(items, ProjectToProto(project))
	}
	return connect.NewResponse(&projectv1.ListProjectsResponse{Projects: items, Page: (commonPage{NextPageToken: page.NextToken}).proto()}), nil
}

func (h *Handler) UpdateProject(ctx context.Context, req *connect.Request[projectv1.UpdateProjectRequest]) (*connect.Response[projectv1.UpdateProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
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
	return connect.NewResponse(&projectv1.UpdateProjectResponse{Project: ProjectToProto(project)}), nil
}

func (h *Handler) ArchiveProject(ctx context.Context, req *connect.Request[projectv1.ArchiveProjectRequest]) (*connect.Response[projectv1.ArchiveProjectResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
	}
	project, err := h.service.Archive(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetExpectedRevision())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&projectv1.ArchiveProjectResponse{Project: ProjectToProto(project)}), nil
}

// errUnauthenticated is the fixed unauthenticated verdict; the identity
// context's own error text never reaches the wire.
var errUnauthenticated = errors.New("authentication is required")

// mapError converts Project failures to Connect codes with fixed, sanitized
// messages: no SQL, driver, constraint, digest, owner, or input details ever
// reach the wire. Unknown failures fall through to the sanitized Internal
// default.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("project request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("project is not available"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("project revision conflict"))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("project service is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("project operation failed"))
	}
}

// ProjectToProto maps the Project domain entity for Core-owned public transports.
func ProjectToProto(project domain.Project) *projectv1.Project {
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

// workspaceFromProto maps references to the domain shape. The enum's String
// form is the domain kind: UNSPECIFIED and undeclared numeric values produce
// strings outside the domain's closed kind set and are rejected there, and a
// nil repeated item degrades to that same invalid verdict instead of
// panicking.
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
