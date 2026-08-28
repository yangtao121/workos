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
	session, err := h.service.Create(ctx, application.CreateCommand{
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
	return connect.NewResponse(&surfacev1.CreateSurfaceResponse{Session: SessionToProto(session)}), nil
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
// same-origin relative path only; the bridge token stays empty and every
// unimplemented capability flag stays false.
func SessionToProto(session domain.SurfaceSession) *surfacev1.SurfaceSession {
	return &surfacev1.SurfaceSession{
		Id: session.ID, AppInstanceId: session.AppInstanceID, ProjectId: session.ProjectID,
		Renderer:  surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		Url:       session.Path,
		CreatedAt: timestamppb.New(session.CreatedAt),
		ExpiresAt: timestamppb.New(session.ExpiresAt),
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
// grammar: only UNSPECIFIED (empty; the application defaults to the
// implemented renderer) and WEB_BUNDLE enter the resolver. Every declared
// but unimplemented renderer and any unknown numeric value is rejected, so a
// client can never silently start a web bundle by naming another renderer.
func preferredRendererFromProto(renderer surfacev1.SurfaceRenderer) (string, error) {
	switch renderer {
	case surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED:
		return "", nil
	case surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE:
		return domain.RendererWebBundle, nil
	default:
		return "", errors.New("preferred renderer is not supported")
	}
}

// mapError converts surface failures to Connect codes with sanitized
// messages: no SQL, paths, manifests, or bundle content. Transient store or
// resolver failures are Unavailable; unknown and invariant failures stay
// Internal.
func mapError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("surface request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("surface session or installed app is not available"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("idempotency key was already used for a different request"))
	case errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("installed app version has no supported web bundle"))
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, ports.ErrStoreUnavailable), errors.Is(err, ports.ErrResolverUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("surface is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("surface operation failed"))
	}
}
