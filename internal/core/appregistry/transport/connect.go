package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// MaxRequestBytes bounds every AppRegistryService request message before the
// Connect stack decodes it. It must accommodate the legal 256 KiB manifest in
// every accepted wire form: binary protobuf framing (~5 bytes of tag and
// length plus the 128-byte idempotency key ceiling) and the protojson
// encoding, where the bytes manifest field inflates 4/3 through base64 to
// ~350 KiB before field names and JSON punctuation. 384 KiB (393,216 bytes)
// leaves headroom over both while staying a small explicit constant — the
// library default is unlimited. The application-level 256 KiB manifest check
// stays in place, so the wire budget only guards decode-time memory.
const MaxRequestBytes = 384 * 1024

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

// NewConnectHandler wires the transport into a real Connect handler with the
// registry's bounded-read configuration. Composition roots and tests must use
// this constructor so the read limit is identical in production and tests;
// the limit applies per decompressed request message and rejects oversize
// bodies with ResourceExhausted before any business code runs.
func NewConnectHandler(service *application.Service) (string, http.Handler) {
	return appv1connect.NewAppRegistryServiceHandler(
		New(service),
		connect.WithReadMaxBytes(MaxRequestBytes),
	)
}

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
	return connect.NewResponse(&appv1.RegisterAppResponse{App: AppToProto(SummaryToProto(record))}), nil
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
	return connect.NewResponse(&appv1.GetAppResponse{App: AppToProto(SummaryToProto(record))}), nil
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
	result, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), pageToken, pageSize)
	if err != nil {
		return nil, mapError(err)
	}
	apps := make([]*appv1.WorkOSApp, 0, len(result.Items))
	for _, summary := range result.Items {
		apps = append(apps, AppToProto(SummaryToProto(summary)))
	}
	// The application layer owns the effective page size and the limit+1
	// probe; transport forwards its next token verbatim.
	return connect.NewResponse(&appv1.ListAppsResponse{
		Apps: apps, Page: &commonv1.PageResponse{NextPageToken: result.NextToken},
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

func SummaryToProto(summary domain.AppVersionSummary) AppSummary {
	return AppSummary{
		ID: summary.AppID, Name: summary.Name, Version: summary.Version,
		Scope: summary.Scope, Permissions: summary.Permissions, ManifestDigest: summary.ManifestDigest,
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
