package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

func (h *Handler) ValidateManifest(ctx context.Context, req *connect.Request[appv1.ValidateManifestRequest]) (*connect.Response[appv1.ValidateManifestResponse], error) {
	if _, err := identity.FromContext(ctx); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if len(req.Msg.GetYaml()) > domain.MaxManifestBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("app manifest exceeds the size limit"))
	}
	manifest, violations, err := h.service.ValidateManifest(ctx, req.Msg.GetYaml())
	if err != nil {
		return nil, mapError(err)
	}
	response := &appv1.ValidateManifestResponse{Valid: len(violations) == 0, Violations: violations}
	if response.Valid {
		response.Normalized = AppToProto(manifestProjection(manifest))
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) RegisterApp(ctx context.Context, req *connect.Request[appv1.RegisterAppRequest]) (*connect.Response[appv1.RegisterAppResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if len(req.Msg.GetManifestYaml()) > domain.MaxManifestBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("app manifest exceeds the size limit"))
	}
	record, err := h.service.Register(ctx, id.UserID, req.Msg.GetIdempotencyKey(), req.Msg.GetManifestYaml())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&appv1.RegisterAppResponse{App: AppToProto(versionToProto(record))}), nil
}

func (h *Handler) GetApp(ctx context.Context, req *connect.Request[appv1.GetAppRequest]) (*connect.Response[appv1.GetAppResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	record, err := h.service.Get(ctx, id.UserID, req.Msg.GetAppId(), req.Msg.GetVersion())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&appv1.GetAppResponse{App: AppToProto(versionToProto(record))}), nil
}

func (h *Handler) ListApps(ctx context.Context, req *connect.Request[appv1.ListAppsRequest]) (*connect.Response[appv1.ListAppsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize, pageToken := 0, ""
	if req.Msg.GetPage() != nil {
		pageSize, pageToken = int(req.Msg.GetPage().GetPageSize()), req.Msg.GetPage().GetPageToken()
	}
	records, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), pageToken, pageSize)
	if err != nil {
		return nil, mapError(err)
	}
	apps := make([]*appv1.WorkOSApp, 0, len(records))
	for _, record := range records {
		apps = append(apps, AppToProto(versionToProto(record)))
	}
	next := ""
	if pageSize > 0 && len(apps) == pageSize {
		next = apps[len(apps)-1].GetId()
	}
	return connect.NewResponse(&appv1.ListAppsResponse{
		Apps: apps, Page: &commonv1.PageResponse{NextPageToken: next},
	}), nil
}

// mapError converts domain failures to Connect codes with sanitized messages:
// no SQL, constraint names, file paths, raw YAML, or validator internals.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("app manifest or request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("app is not registered for this owner"))
	case errors.Is(err, domain.ErrVersionExists):
		return connect.NewError(connect.CodeAlreadyExists, errors.New("app version is already registered with a different manifest"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("app registry operation failed"))
	}
}

// AppSummary is the deterministic public projection of one registered fact.
type AppSummary struct {
	ID             string
	Name           string
	Version        string
	Scope          domain.Scope
	Permissions    []string
	ManifestDigest string
}

func manifestProjection(manifest domain.Manifest) AppSummary {
	return AppSummary{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Scope: manifest.Scope, Permissions: manifest.Permissions, ManifestDigest: manifest.Digest,
	}
}

func versionToProto(record domain.AppVersion) AppSummary {
	return AppSummary{
		ID: record.AppID, Name: record.Name, Version: record.Version,
		Scope: record.Scope, Permissions: record.Permissions, ManifestDigest: record.ManifestDigest,
	}
}

// AppToProto maps the projection to the public WorkOSApp message.
func AppToProto(summary AppSummary) *appv1.WorkOSApp {
	return &appv1.WorkOSApp{
		Id: summary.ID, Name: summary.Name, Version: summary.Version,
		Scope: scopeToProto(summary.Scope), Permissions: summary.Permissions,
		ManifestDigest: summary.ManifestDigest,
	}
}

func scopeToProto(scope domain.Scope) appv1.AppScope {
	switch scope {
	case domain.ScopeUser:
		return appv1.AppScope_APP_SCOPE_USER
	case domain.ScopeProject:
		return appv1.AppScope_APP_SCOPE_PROJECT
	case domain.ScopeSystem:
		return appv1.AppScope_APP_SCOPE_SYSTEM
	default:
		return appv1.AppScope_APP_SCOPE_UNSPECIFIED
	}
}
