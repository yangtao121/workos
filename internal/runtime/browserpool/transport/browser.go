// Package transport serves the Remote Browser Pool RPCs (ADR-0027).
package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/browserpool/application"
	"github.com/yangtao121/workos/internal/runtime/browserpool/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type BrowserPoolHandler struct{ service *application.Service }

func NewBrowserPoolHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewBrowserSessionServiceHandler(&BrowserPoolHandler{service: service}, connect.WithReadMaxBytes(64*1024))
}

func sessionProto(session domain.Session, engine string) *surfacev1.BrowserSession {
	return &surfacev1.BrowserSession{
		Id: session.SessionID, OwnerUserId: session.OwnerUserID, ProjectId: session.ProjectID,
		State: string(session.State), CurrentUrl: session.CurrentURL,
		Engine: engine, RestartCount: session.RestartCount,
		CreatedAt: timestamppb.New(session.CreatedAt), ExpiresAt: timestamppb.New(session.ExpiresAt),
	}
}

func (h *BrowserPoolHandler) CreateBrowserSession(ctx context.Context, req *connect.Request[surfacev1.CreateBrowserSessionRequest]) (*connect.Response[surfacev1.CreateBrowserSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Create(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIdempotencyKey(), req.Msg.GetInitialUrl())
	if err != nil {
		return nil, browserPoolError(err)
	}
	return connect.NewResponse(&surfacev1.CreateBrowserSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *BrowserPoolHandler) NavigateBrowserSession(ctx context.Context, req *connect.Request[surfacev1.NavigateBrowserSessionRequest]) (*connect.Response[surfacev1.NavigateBrowserSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Navigate(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetUrl())
	if err != nil {
		return nil, browserPoolError(err)
	}
	return connect.NewResponse(&surfacev1.NavigateBrowserSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *BrowserPoolHandler) CloseBrowserSession(ctx context.Context, req *connect.Request[surfacev1.CloseBrowserSessionRequest]) (*connect.Response[surfacev1.CloseBrowserSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Close(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, browserPoolError(err)
	}
	return connect.NewResponse(&surfacev1.CloseBrowserSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *BrowserPoolHandler) GetBrowserSession(ctx context.Context, req *connect.Request[surfacev1.GetBrowserSessionRequest]) (*connect.Response[surfacev1.GetBrowserSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Get(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, browserPoolError(err)
	}
	return connect.NewResponse(&surfacev1.GetBrowserSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

// WatchBrowserSession streams bounded screencast frames: one capture per
// interval, sent as JPEG frames with sequence numbers. The stream ends when
// the session closes or the client disconnects.
func (h *BrowserPoolHandler) WatchBrowserSession(ctx context.Context, req *connect.Request[surfacev1.WatchBrowserSessionRequest], stream *connect.ServerStream[surfacev1.WatchBrowserSessionResponse]) error {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Get(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return browserPoolError(err)
	}
	slog.Info("browser watch opened", "session", req.Msg.GetSessionId(), "state", session.State)
	if err := stream.Send(&surfacev1.WatchBrowserSessionResponse{Kind: &surfacev1.WatchBrowserSessionResponse_State{State: sessionProto(session, h.service.Facts().Engine)}}); err != nil {
		return err
	}
	ticker := time.NewTicker(domain.FrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			frame, err := h.service.Capture(ctx, owner.UserID, req.Msg.GetSessionId())
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return nil
				}
				// Transient capture failures (mid-navigation renderer swaps)
				// skip this tick; the stream keeps serving the live session.
				continue
			}
			if err := stream.Send(&surfacev1.WatchBrowserSessionResponse{Kind: &surfacev1.WatchBrowserSessionResponse_Frame{Frame: &surfacev1.BrowserFrame{
				Sequence: frame.Sequence, Jpeg: frame.JPEG, Width: frame.Width, Height: frame.Height,
			}}}); err != nil {
				return err
			}
		}
	}
}

func browserPoolError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrIdempotencyDrift):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrSessionLimit):
		code = connect.CodeResourceExhausted
	case errors.Is(err, domain.ErrEngineUnavailable), errors.Is(err, domain.ErrStoreUnavailable):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("browser session request failed"))
}
