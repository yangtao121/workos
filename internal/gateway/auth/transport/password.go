package transport

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/yangtao121/workos/gen/go/workos/auth/v1"
	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

// PasswordHandler serves mode discovery in either production mode; Login is
// enabled only when the Gateway selected password authentication at startup.
type PasswordHandler struct {
	app *application.PasswordService
	now func() time.Time
}

func NewPasswordHandler(app *application.PasswordService, now func() time.Time) *PasswordHandler {
	return &PasswordHandler{app: app, now: now}
}

func (h *PasswordHandler) GetMode(_ context.Context, _ *connect.Request[authv1.GetModeRequest]) (*connect.Response[authv1.GetModeResponse], error) {
	mode := authv1.AuthMode_AUTH_MODE_PAIRING
	if h.app != nil {
		mode = authv1.AuthMode_AUTH_MODE_PASSWORD
	}
	response := connect.NewResponse(&authv1.GetModeResponse{Mode: mode})
	noStore(response.Header())
	return response, nil
}

func (h *PasswordHandler) Login(ctx context.Context, req *connect.Request[authv1.LoginRequest]) (*connect.Response[authv1.LoginResponse], error) {
	if h.app == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("password login is unavailable in pairing mode"))
	}
	result, err := h.app.Login(ctx, req.Msg.GetUsername(), req.Msg.GetPassword(), req.Msg.GetDeviceName(), deviceClassFromProto(req.Msg.GetDeviceClass()))
	if err != nil {
		return nil, verdict(err)
	}
	writer, ok := connectHTTPWriter(ctx)
	if !ok {
		return nil, verdict(domain.ErrAuthCorrupt)
	}
	SetSessionCookie(writer, result.SessionToken, result.SessionExpires, h.now())
	response := connect.NewResponse(&authv1.LoginResponse{Device: deviceInfo(result.Device, true), SessionExpiresAt: timestamp(result.SessionExpires)})
	noStore(response.Header())
	return response, nil
}

var _ authv1connect.PasswordAuthServiceHandler = (*PasswordHandler)(nil)
