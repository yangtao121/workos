package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// InstallationHandler serves the public AppInstallationService. The owner
// comes only from the identity context; responses expose the pinned registry
// identity, never manifests, credentials, or permission grants.
type InstallationHandler struct {
	service *application.InstallationService
}

func NewInstallationHandler(service *application.InstallationService) *InstallationHandler {
	return &InstallationHandler{service: service}
}

// MaxRequestBytes bounds every AppInstallationService request message before
// the Connect stack decodes it. The capability grammar puts no length cap on
// a single granted permission, but a legal grant set is always a subset of
// the pinned manifest version's requested permissions, and those strings —
// like the requested version itself — come from a registry manifest of at
// most 256 KiB, so grant and version content together never exceed that
// document. The requests carry no bytes fields, so no base64 inflation needs
// covering: binary protobuf adds only framing, and protojson adds field
// names, JSON punctuation, and the scalar ceilings (the 128-rune idempotency
// key, 63-byte app ID, and UUID fields) — a few KiB in every accepted wire
// form. 288 KiB (294,912 bytes) holds the 256 KiB content ceiling with 32 KiB
// of headroom while staying a small explicit constant — the library default
// is unlimited. The application-level grant shape and requested-subset
// checks stay in place, so the wire budget only guards decode-time memory.
const MaxRequestBytes = 288 * 1024

// NewInstallationConnectHandler wires the transport into a real Connect
// handler with the bounded-read configuration. Composition roots and tests
// must use this constructor so the read limit is identical in production and
// tests; the limit applies per decompressed request message and rejects
// oversize bodies with ResourceExhausted before any business code runs.
func NewInstallationConnectHandler(service *application.InstallationService) (string, http.Handler) {
	return appv1connect.NewAppInstallationServiceHandler(
		NewInstallationHandler(service),
		connect.WithReadMaxBytes(MaxRequestBytes),
	)
}

func (h *InstallationHandler) InstallApp(ctx context.Context, req *connect.Request[appv1.InstallAppRequest]) (*connect.Response[appv1.InstallAppResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.Install(ctx, application.InstallInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(),
		ProjectID: req.Msg.GetProjectId(), AppID: req.Msg.GetAppId(),
		Version: req.Msg.GetVersion(), ExpectedRevision: req.Msg.GetExpectedProjectRevision(),
		GrantedPermissions: req.Msg.GetGrantedPermissions(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return connect.NewResponse(&appv1.InstallAppResponse{
		Installation: InstallationToProto(result.Installation), ProjectRevision: result.ProjectRevision,
	}), nil
}

func (h *InstallationHandler) UninstallApp(ctx context.Context, req *connect.Request[appv1.UninstallAppRequest]) (*connect.Response[appv1.UninstallAppResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.Uninstall(ctx, application.UninstallInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(),
		ProjectID: req.Msg.GetProjectId(), InstallationID: req.Msg.GetInstallationId(),
		ExpectedRevision: req.Msg.GetExpectedProjectRevision(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return connect.NewResponse(&appv1.UninstallAppResponse{
		Installation: InstallationToProto(result.Installation), ProjectRevision: result.ProjectRevision,
	}), nil
}

func (h *InstallationHandler) ListInstalledApps(ctx context.Context, req *connect.Request[appv1.ListInstalledAppsRequest]) (*connect.Response[appv1.ListInstalledAppsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize, pageToken := 0, ""
	if req.Msg.GetPage() != nil {
		pageSize, pageToken = int(req.Msg.GetPage().GetPageSize()), req.Msg.GetPage().GetPageToken()
	}
	page, err := h.service.ListInstalled(ctx, id.UserID, req.Msg.GetProjectId(), pageToken, pageSize)
	if err != nil {
		return nil, mapInstallationError(err)
	}
	installations := make([]*appv1.AppInstallation, 0, len(page.Items))
	for _, installation := range page.Items {
		installations = append(installations, InstallationToProto(installation))
	}
	// The application layer owns the effective page size and the limit+1
	// probe; transport forwards its next token verbatim.
	return connect.NewResponse(&appv1.ListInstalledAppsResponse{
		Installations: installations,
		Page:          &commonv1.PageResponse{NextPageToken: page.NextToken},
	}), nil
}

// SetAppGrants is the full-replacement grant command (ADR-0003). The owner
// comes only from the identity context; the adjudication — canonical target,
// exact pinned version subset ceiling, replay, revision arbitration — lives
// in the application and repository.
func (h *InstallationHandler) SetAppGrants(ctx context.Context, req *connect.Request[appv1.SetAppGrantsRequest]) (*connect.Response[appv1.SetAppGrantsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.SetAppGrants(ctx, application.SetAppGrantsInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(),
		ProjectID: req.Msg.GetProjectId(), InstallationID: req.Msg.GetInstallationId(),
		ExpectedRevision:   req.Msg.GetExpectedProjectRevision(),
		GrantedPermissions: req.Msg.GetGrantedPermissions(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return connect.NewResponse(&appv1.SetAppGrantsResponse{
		Installation: InstallationToProto(result.Installation), ProjectRevision: result.ProjectRevision,
	}), nil
}

// TransitionAppVersion pins one explicit immutable registry version onto the
// active installation (ADR-0012). The owner comes only from the identity
// context; the target digest is re-resolved server-side and the client can
// never submit one.
func (h *InstallationHandler) TransitionAppVersion(ctx context.Context, req *connect.Request[appv1.TransitionAppVersionRequest]) (*connect.Response[appv1.TransitionAppVersionResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.Transition(ctx, application.TransitionInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(),
		ProjectID: req.Msg.GetProjectId(), InstallationID: req.Msg.GetInstallationId(),
		ExpectedRevision: req.Msg.GetExpectedProjectRevision(), Version: req.Msg.GetVersion(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return connect.NewResponse(&appv1.TransitionAppVersionResponse{
		Installation: InstallationToProto(result.Installation), ProjectRevision: result.ProjectRevision,
	}), nil
}

// RollbackAppVersion restores the most recent previous pinned snapshot that
// differs from the current identity (ADR-0012). The target is derived
// server-side from the durable history; the request carries none.
func (h *InstallationHandler) RollbackAppVersion(ctx context.Context, req *connect.Request[appv1.RollbackAppVersionRequest]) (*connect.Response[appv1.RollbackAppVersionResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.Rollback(ctx, application.RollbackInput{
		OwnerUserID: id.UserID, IdempotencyKey: req.Msg.GetIdempotencyKey(),
		ProjectID: req.Msg.GetProjectId(), InstallationID: req.Msg.GetInstallationId(),
		ExpectedRevision: req.Msg.GetExpectedProjectRevision(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return connect.NewResponse(&appv1.RollbackAppVersionResponse{
		Installation: InstallationToProto(result.Installation), ProjectRevision: result.ProjectRevision,
		RolledBackToVersion: result.Installation.Version,
	}), nil
}

// ListAppVersionHistory reads one installation's bounded version history,
// oldest first, with application-owned limit+1 pagination.
func (h *InstallationHandler) ListAppVersionHistory(ctx context.Context, req *connect.Request[appv1.ListAppVersionHistoryRequest]) (*connect.Response[appv1.ListAppVersionHistoryResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize, pageToken := 0, ""
	if req.Msg.GetPage() != nil {
		pageSize, pageToken = int(req.Msg.GetPage().GetPageSize()), req.Msg.GetPage().GetPageToken()
	}
	page, err := h.service.ListVersionHistory(ctx, id.UserID, req.Msg.GetProjectId(), req.Msg.GetInstallationId(), pageToken, pageSize)
	if err != nil {
		return nil, mapInstallationError(err)
	}
	snapshots := make([]*appv1.AppInstallationVersionSnapshot, 0, len(page.Items))
	for _, snapshot := range page.Items {
		snapshots = append(snapshots, VersionSnapshotToProto(snapshot))
	}
	return connect.NewResponse(&appv1.ListAppVersionHistoryResponse{
		Snapshots: snapshots,
		Page:      &commonv1.PageResponse{NextPageToken: page.NextToken},
	}), nil
}

// mapInstallationError converts installation failures to Connect codes with
// sanitized messages: no SQL, constraint names, catalog internals, grant
// contents, or owner details. Stored-fact corruption and pinned-identity
// drift fall through to the sanitized Internal default; a temporarily
// unreachable Project store (or catalog dependency surfaced through the
// neutral port as the same sentinel) stays a retryable Unavailable.
func mapInstallationError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("app installation request is invalid"))
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, application.ErrAppNotInstallable):
		return connect.NewError(connect.CodeNotFound, errors.New("project, app, or installation is not available"))
	case errors.Is(err, domain.ErrAlreadyInstalled):
		return connect.NewError(connect.CodeAlreadyExists, errors.New("app is already installed with a different version"))
	case errors.Is(err, domain.ErrInvalidGrant):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("granted permissions are malformed"))
	case errors.Is(err, domain.ErrGrantNotRequested):
		return connect.NewError(connect.CodePermissionDenied, errors.New("granted permission was not requested by the app"))
	case errors.Is(err, domain.ErrGrantNotCompatible):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("current permissions are not compatible with the target version; review permissions first"))
	case errors.Is(err, domain.ErrNoPreviousVersion):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no previous version to roll back to"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("project revision conflict"))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("app installation service is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("app installation operation failed"))
	}
}

// InstallationToProto maps the installation domain entity to the public
// projection, including the current canonical grant set and its epoch.
func InstallationToProto(installation domain.Installation) *appv1.AppInstallation {
	result := &appv1.AppInstallation{
		Id: installation.ID, ProjectId: installation.ProjectID, AppId: installation.AppID,
		Version: installation.Version, ManifestDigest: installation.ManifestDigest,
		GrantedPermissions: installation.GrantedPermissions,
		GrantRevision:      installation.GrantRevision,
		InstalledAt:        timestamppb.New(installation.InstalledAt),
	}
	if installation.UninstalledAt != nil {
		result.UninstalledAt = timestamppb.New(*installation.UninstalledAt)
	}
	return result
}

// VersionSnapshotToProto maps one history snapshot to the public projection.
func VersionSnapshotToProto(snapshot domain.VersionSnapshot) *appv1.AppInstallationVersionSnapshot {
	return &appv1.AppInstallationVersionSnapshot{
		Version:        snapshot.Version,
		ManifestDigest: snapshot.ManifestDigest,
		Source:         snapshot.Source,
		Sequence:       snapshot.Sequence,
		OccurredAt:     timestamppb.New(snapshot.OccurredAt),
	}
}
