// Private transport of the Core app notification ingest service (ADR-0014).
// Only runtime-host reaches these RPCs after validating the bridge token,
// surface session, and grant epoch; the gateway allowlist never routes this
// service. Errors are sanitized and never echo app-supplied text.
package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	"github.com/yangtao121/workos/internal/core/notification/application"
	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type ingestHandler struct {
	service *application.Service
}

// NewIngestConnectHandler wires the private ingest transport with a tight
// pre-decode budget: the largest legal request is one bounded title, one
// bounded body, and canonical identifiers — far below 16 KiB.
func NewIngestConnectHandler(service *application.Service) (string, http.Handler) {
	return notificationv1connect.NewAppNotificationIngestServiceHandler(
		&ingestHandler{service: service},
		connect.WithReadMaxBytes(16*1024),
	)
}

func (h *ingestHandler) CreateAppNotification(ctx context.Context, req *connect.Request[notificationv1.CreateAppNotificationRequest]) (*connect.Response[notificationv1.CreateAppNotificationResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("trusted identity is required"))
	}
	result, err := h.service.CreateAppNotification(ctx, application.CreateAppNotificationInput{
		OwnerUserID:               id.UserID,
		ProjectID:                 req.Msg.GetProjectId(),
		AppInstanceID:             req.Msg.GetAppInstanceId(),
		InstallationGrantRevision: req.Msg.GetInstallationGrantRevision(),
		IdempotencyKey:            req.Msg.GetIdempotencyKey(),
		Title:                     req.Msg.GetTitle(),
		Body:                      req.Msg.GetBody(),
	})
	if err != nil {
		return nil, mapIngestError(err)
	}
	return connect.NewResponse(&notificationv1.CreateAppNotificationResponse{
		Notification:   notificationProto(result.Notification),
		ChangeSequence: result.ChangeSequence,
		UnreadCount:    result.UnreadCount,
	}), nil
}

func mapIngestError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, application.ErrInvalid), errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("app notification request is invalid"))
	case errors.Is(err, application.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("app notification request conflicts with a consumed key"))
	case errors.Is(err, application.ErrAppExhausted):
		return connect.NewError(connect.CodeResourceExhausted, errors.New("app notification allowance is exhausted"))
	case errors.Is(err, ports.ErrAppNotificationDenied):
		return connect.NewError(connect.CodePermissionDenied, errors.New("app notification is not authorized"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("notification is not available"))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("notification store is temporarily unavailable"))
	case errors.Is(err, domain.ErrCorrupt), errors.Is(err, ports.ErrSourceDigestDrift):
		return connect.NewError(connect.CodeInternal, errors.New("notification operation failed"))
	}
	return connect.NewError(connect.CodeInternal, errors.New("notification operation failed"))
}
