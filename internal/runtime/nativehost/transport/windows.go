package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/nativehost/application"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

type WindowHandler struct{ service *application.Service }

func NewWindowHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewGreenfieldWindowServiceHandler(&WindowHandler{service: service}, connect.WithReadMaxBytes(1_500_000))
}

func (h *WindowHandler) WatchGreenfieldWindows(ctx context.Context, req *connect.Request[surfacev1.WatchGreenfieldWindowsRequest], stream *connect.ServerStream[surfacev1.WatchGreenfieldWindowsResponse]) error {
	caller, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("identity required"))
	}
	view, err := h.service.OpenWindowSnapshots(ctx, caller.UserID, caller.DeviceID, req.Msg.GetSessionId(), req.Msg.GetAttachmentId(), req.Msg.GetExpectedWorkloadGeneration())
	if err != nil {
		return windowError(err)
	}
	defer view.Close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := view.Revalidate(ctx); err != nil {
				return windowError(err)
			}
		case snapshot, ok := <-view.Windows:
			if !ok {
				return windowError(domain.ErrEngineUnavailable)
			}
			if err := view.Revalidate(ctx); err != nil {
				return windowError(err)
			}
			if err := stream.Send(&surfacev1.WatchGreenfieldWindowsResponse{Snapshot: snapshot}); err != nil {
				return err
			}
		}
	}
}

func (h *WindowHandler) WatchGreenfieldWindowFrames(ctx context.Context, req *connect.Request[surfacev1.WatchGreenfieldWindowFramesRequest], stream *connect.ServerStream[surfacev1.WatchGreenfieldWindowFramesResponse]) error {
	caller, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("identity required"))
	}
	view, err := h.service.OpenWindowFrames(ctx, caller.UserID, caller.DeviceID, req.Msg.GetSessionId(), req.Msg.GetAttachmentId(), req.Msg.GetExpectedWorkloadGeneration(), req.Msg.GetWindowId())
	if err != nil {
		return windowError(err)
	}
	defer view.Close()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	first := time.NewTimer(10 * time.Second)
	defer first.Stop()
	firstSent := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := view.Revalidate(ctx); err != nil {
				return windowError(err)
			}
		case <-first.C:
			if !firstSent {
				return windowError(domain.ErrEngineUnavailable)
			}
		case frame, ok := <-view.Frames:
			if !ok {
				return windowError(domain.ErrEngineUnavailable)
			}
			if len(frame) == 0 || (!firstSent && !frame[0].GetFullRefresh()) {
				return windowError(domain.ErrEngineUnavailable)
			}
			started := time.Now()
			for _, tile := range frame {
				if err := view.Revalidate(ctx); err != nil {
					return windowError(err)
				}
				if err := stream.Send(&surfacev1.WatchGreenfieldWindowFramesResponse{Tile: tile}); err != nil {
					return err
				}
				if time.Since(started) > 5*time.Second {
					return connect.NewError(connect.CodeResourceExhausted, errors.New("window viewer too slow"))
				}
			}
			firstSent = true
		}
	}
}

func (h *WindowHandler) SendGreenfieldWindowInput(ctx context.Context, req *connect.Request[surfacev1.SendGreenfieldWindowInputRequest]) (*connect.Response[surfacev1.SendGreenfieldWindowInputResponse], error) {
	caller, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("identity required"))
	}
	result, err := h.service.SendWindowInput(ctx, caller.UserID, caller.DeviceID, req.Msg)
	if err != nil {
		return nil, windowError(err)
	}
	return connect.NewResponse(result), nil
}

func (h *WindowHandler) ReadGreenfieldClipboard(ctx context.Context, req *connect.Request[surfacev1.ReadGreenfieldClipboardRequest]) (*connect.Response[surfacev1.ReadGreenfieldClipboardResponse], error) {
	caller, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("identity required"))
	}
	result, err := h.service.ReadWindowClipboard(ctx, caller.UserID, caller.DeviceID, req.Msg)
	if err != nil {
		return nil, windowError(err)
	}
	return connect.NewResponse(result), nil
}

func windowError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrControlDenied):
		code = connect.CodePermissionDenied
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrSessionLimit):
		code = connect.CodeResourceExhausted
	case errors.Is(err, domain.ErrEngineUnavailable), errors.Is(err, domain.ErrStoreUnavailable), errors.Is(err, domain.ErrResidentChildNotFound):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("greenfield window request failed"))
}

var _ surfacev1connect.GreenfieldWindowServiceHandler = (*WindowHandler)(nil)
