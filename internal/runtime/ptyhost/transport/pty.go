// Package transport serves the supervised PTY RPCs (ADR-0028).
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/application"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PtyHandler struct{ service *application.Service }

func NewPtyHandler(service *application.Service) (string, http.Handler) {
	return surfacev1connect.NewPtySessionServiceHandler(&PtyHandler{service: service}, connect.WithReadMaxBytes(128*1024))
}

func sessionProto(session domain.Session, engine string) *surfacev1.PtySession {
	return &surfacev1.PtySession{
		Id: session.SessionID, OwnerUserId: session.OwnerUserID, ProjectId: session.ProjectID,
		State: string(session.State), Engine: engine,
		CreatedAt: timestamppb.New(session.CreatedAt), ExpiresAt: timestamppb.New(session.ExpiresAt),
	}
}

func (h *PtyHandler) CreatePtySession(ctx context.Context, req *connect.Request[surfacev1.CreatePtySessionRequest]) (*connect.Response[surfacev1.CreatePtySessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Create(ctx, owner.UserID, req.Msg.GetProjectId(), req.Msg.GetIdempotencyKey(), req.Msg.GetColumns(), req.Msg.GetRows())
	if err != nil {
		return nil, ptyError(err)
	}
	return connect.NewResponse(&surfacev1.CreatePtySessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *PtyHandler) WritePtySession(ctx context.Context, req *connect.Request[surfacev1.WritePtySessionRequest]) (*connect.Response[surfacev1.WritePtySessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Write(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetInput())
	if err != nil {
		return nil, ptyError(err)
	}
	return connect.NewResponse(&surfacev1.WritePtySessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *PtyHandler) ReadPtySession(ctx context.Context, req *connect.Request[surfacev1.ReadPtySessionRequest]) (*connect.Response[surfacev1.ReadPtySessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	cursor, output, closed, err := h.service.Read(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetAfter(), req.Msg.GetMaxBytes())
	if err != nil {
		return nil, ptyError(err)
	}
	return connect.NewResponse(&surfacev1.ReadPtySessionResponse{Cursor: cursor, Output: output, Closed: closed}), nil
}

func (h *PtyHandler) ResizePtySession(ctx context.Context, req *connect.Request[surfacev1.ResizePtySessionRequest]) (*connect.Response[surfacev1.ResizePtySessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Resize(ctx, owner.UserID, req.Msg.GetSessionId(), req.Msg.GetColumns(), req.Msg.GetRows())
	if err != nil {
		return nil, ptyError(err)
	}
	return connect.NewResponse(&surfacev1.ResizePtySessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func (h *PtyHandler) ClosePtySession(ctx context.Context, req *connect.Request[surfacev1.ClosePtySessionRequest]) (*connect.Response[surfacev1.ClosePtySessionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	session, err := h.service.Close(ctx, owner.UserID, req.Msg.GetSessionId())
	if err != nil {
		return nil, ptyError(err)
	}
	return connect.NewResponse(&surfacev1.ClosePtySessionResponse{Session: sessionProto(session, h.service.Facts().Engine)}), nil
}

func ptyError(err error) error {
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
	return connect.NewError(code, errors.New("pty session request failed"))
}
