// Package transport exposes the Surface Broker over the public Connect
// SurfaceService and the same-origin /surfaces/ static asset routes.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/application"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type Handler struct{ service *application.Service }

func New(service *application.Service) *Handler { return &Handler{service: service} }

// NewConnectHandler wires the transport into a real Connect handler.
func NewConnectHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewSurfaceServiceHandler(New(service))
}

func (h *Handler) CreateSurface(ctx context.Context, req *connect.Request[surfacev1.CreateSurfaceRequest]) (*connect.Response[surfacev1.CreateSurfaceResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	viewport := req.Msg.GetViewport()
	var width, height int32
	var ratio float64
	if viewport != nil {
		width, height, ratio = viewport.GetWidth(), viewport.GetHeight(), viewport.GetPixelRatio()
	}
	renderer, err := preferredRendererFromProto(req.Msg.GetPreferredRenderer())
	if err != nil {
		// Declared-but-unimplemented and unknown enum values fail closed as
		// InvalidArgument before the application runs: no resolver call, no
		// session row, no idempotency key consumption.
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	created, err := h.service.Create(ctx, application.CreateCommand{
		OwnerUserID:       id.UserID,
		DeviceID:          id.DeviceID,
		IdempotencyKey:    req.Msg.GetIdempotencyKey(),
		ProjectID:         req.Msg.GetProjectId(),
		AppInstanceID:     req.Msg.GetAppInstanceId(),
		DeviceClass:       deviceClassFromProto(req.Msg.GetDeviceClass()),
		ViewportWidth:     width,
		ViewportHeight:    height,
		ViewportRatio:     ratio,
		PreferredRenderer: renderer,
	})
	if err != nil {
		return nil, mapError(err)
	}
	// The bridge token rides only in this response: the trusted desktop/app-host
	// keeps it in memory and presents it on App Bridge RPC metadata. It never
	// enters the iframe, URLs, storage, DOM, or logs.
	return connect.NewResponse(&surfacev1.CreateSurfaceResponse{
		Session: SessionToProto(created.Session, created.BridgeToken),
	}), nil
}

func (h *Handler) CloseSurface(ctx context.Context, req *connect.Request[surfacev1.CloseSurfaceRequest]) (*connect.Response[surfacev1.CloseSurfaceResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	if _, err := h.service.Close(ctx, id.UserID, id.DeviceID, req.Msg.GetSurfaceSessionId()); err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&surfacev1.CloseSurfaceResponse{}), nil
}

// SessionToProto projects the durable session fact. The URL is the
// same-origin relative path only. bridgeToken is the ephemeral credential for
// the trusted host (empty when no token is valid — e.g. a closed/expired
// replay); the effective capability list carries only implemented and granted
// methods, and every unimplemented capability flag stays false.
func SessionToProto(session domain.SurfaceSession, bridgeToken string) *surfacev1.SurfaceSession {
	return &surfacev1.SurfaceSession{
		Id: session.ID, AppInstanceId: session.AppInstanceID, ProjectId: session.ProjectID,
		Renderer:           rendererProto(session.Renderer),
		Url:                session.Path,
		BridgeToken:        bridgeToken,
		BridgeCapabilities: session.BridgeCapabilities,
		CreatedAt:          timestamppb.New(session.CreatedAt),
		ExpiresAt:          timestamppb.New(session.ExpiresAt),
	}
}

// rendererProto maps the persisted renderer fact onto the enum. The host
// never projects any other value: an unknown stored renderer is impossible by
// the database CHECK and stays UNSPECIFIED rather than being invented here.
func rendererProto(renderer string) surfacev1.SurfaceRenderer {
	switch renderer {
	case domain.RendererWebBundle:
		return surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE
	case domain.RendererWebService:
		return surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_SERVICE
	default:
		return surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED
	}
}

func deviceClassFromProto(class surfacev1.DeviceClass) string {
	switch class {
	case surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP:
		return "desktop"
	case surfacev1.DeviceClass_DEVICE_CLASS_TABLET:
		return "tablet"
	case surfacev1.DeviceClass_DEVICE_CLASS_FOLDABLE:
		return "foldable"
	case surfacev1.DeviceClass_DEVICE_CLASS_PHONE:
		return "phone"
	default:
		return ""
	}
}

// preferredRendererFromProto maps the declared enum to the canonical renderer
// grammar: UNSPECIFIED (empty; the server selects the renderer from the exact
// pinned descriptor), WEB_BUNDLE, and WEB_SERVICE enter the resolver. Every
// declared but unimplemented renderer and any unknown numeric value is
// rejected, so a client can never silently start a surface by naming another
// renderer.
func preferredRendererFromProto(renderer surfacev1.SurfaceRenderer) (string, error) {
	switch renderer {
	case surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED:
		return "", nil
	case surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE:
		return domain.RendererWebBundle, nil
	case surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_SERVICE:
		return domain.RendererWebService, nil
	default:
		return "", errors.New("preferred renderer is not supported")
	}
}

// mapError converts surface failures to Connect codes with sanitized
// messages: no SQL, paths, manifests, or bundle content. Transient store or
// resolver failures are Unavailable; unknown and invariant failures stay
// Internal. A stale grant-epoch replay is one fixed FailedPrecondition that
// never names the current or stored revision or grants.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("surface request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("surface session or installed app is not available"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrGrantEpochStale):
		// ADR-0003 §3: the caller must reopen with a new create key; the
		// verdict is fixed and content-free.
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("surface grants changed; create a new surface with a new idempotency key"))
	case errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("installed app version has no supported surface renderer"))
	case errors.Is(err, ports.ErrResolverCorrupt):
		// A resolution that violates a stored-fact invariant (e.g. a grant
		// epoch below 1) is corruption, never a client verdict.
		return connect.NewError(connect.CodeInternal, errors.New("surface resolution failed"))
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, ports.ErrStoreUnavailable), errors.Is(err, ports.ErrResolverUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("surface is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("surface operation failed"))
	}
}
