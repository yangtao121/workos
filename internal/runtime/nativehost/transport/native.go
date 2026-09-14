// Package transport serves the virtual-display native session RPCs
// (ADR-0029). The Gateway routes this service with owner identity; SDP
// exchanges stay inside the per-request byte budget.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/nativehost/application"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type NativeHandler struct{ service *application.Service }

func NewNativeHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewNativeSessionServiceHandler(&NativeHandler{service: service}, connect.WithReadMaxBytes(128*1024))
}

func sessionProto(session domain.Session, engine string) *surfacev1.NativeSession {
	return &surfacev1.NativeSession{
		Id: session.SessionID, OwnerUserId: session.OwnerUserID, ProjectId: session.ProjectID,
		State: string(session.State), Engine: engine,
		Width: session.Width, Height: session.Height,
		CreatedAt: timestamppb.New(session.CreatedAt), ExpiresAt: timestamppb.New(session.ExpiresAt),
	}
}

func (h *NativeHandler) CreateNativeSession(ctx context.Context, req *connect.Request[surfacev1.CreateNativeSessionRequest]) (*connect.Response[surfacev1.CreateNativeSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Create(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIdempotencyKey(), req.Msg.GetWidth(), req.Msg.GetHeight())
	if err != nil {
		return nil, nativeError(err)
	}
	return connect.NewResponse(&surfacev1.CreateNativeSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *NativeHandler) ConnectNativeSession(ctx context.Context, req *connect.Request[surfacev1.ConnectNativeSessionRequest]) (*connect.Response[surfacev1.ConnectNativeSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, answer, err := h.service.Connect(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetOfferSdp())
	if err != nil {
		return nil, nativeError(err)
	}
	return connect.NewResponse(&surfacev1.ConnectNativeSessionResponse{
		Session: sessionProto(session, h.service.Facts().Engine), AnswerSdp: answer,
	}), nil
}

func (h *NativeHandler) GetNativeSession(ctx context.Context, req *connect.Request[surfacev1.GetNativeSessionRequest]) (*connect.Response[surfacev1.GetNativeSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Get(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, nativeError(err)
	}
	return connect.NewResponse(&surfacev1.GetNativeSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *NativeHandler) CloseNativeSession(ctx context.Context, req *connect.Request[surfacev1.CloseNativeSessionRequest]) (*connect.Response[surfacev1.CloseNativeSessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Close(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, nativeError(err)
	}
	return connect.NewResponse(&surfacev1.CloseNativeSessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func nativeError(err error) error {
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
	return connect.NewError(code, errors.New("native session request failed"))
}
