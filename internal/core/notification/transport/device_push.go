package transport

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	"github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	"github.com/yangtao121/workos/internal/core/notification/application"
	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type devicePushHandler struct{ service *application.PushService }

func NewDevicePushConnectHandler(service *application.PushService) (string, http.Handler) {
	return notificationv1connect.NewDevicePushServiceHandler(&devicePushHandler{service: service}, connect.WithReadMaxBytes(1024))
}

func (h *devicePushHandler) RevokeDevicePush(ctx context.Context, req *connect.Request[notificationv1.RevokeDevicePushRequest]) (*connect.Response[notificationv1.RevokeDevicePushResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil || req.Msg.IdempotencyKey != id.DeviceID {
		return nil, mapPushError(domain.ErrPushInvalid)
	}
	if req.Msg.RevokedAt == nil || req.Msg.RevokedAt.CheckValid() != nil {
		return nil, mapPushError(domain.ErrPushInvalid)
	}
	if err := h.service.RevokeDevice(ctx, req.Msg.RevokedAt.AsTime()); err != nil {
		return nil, mapPushError(err)
	}
	return connect.NewResponse(&notificationv1.RevokeDevicePushResponse{}), nil
}
