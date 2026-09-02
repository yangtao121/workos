// Public transport of the Core NotificationService. Owner/device identity
// comes exclusively from the gateway-injected trusted context — never from
// the request body. Watch streams are bounded in lifetime and per-owner in
// concurrency; heartbeats and lifetime ends never advance the durable
// cursor, and swept cursors are answered with RESET_REQUIRED instead of a
// silent resume (ADR-0014).
package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	notificationv1 "github.com/yangtao121/workos/gen/go/workos/notification/v1"
	notificationv1connect "github.com/yangtao121/workos/gen/go/workos/notification/v1/notificationv1connect"
	"github.com/yangtao121/workos/internal/core/notification/application"
	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MaxRequestBytes bounds every public notification request before decode.
// The largest legal request carries 100 canonical UUIDs plus a bounded
// key — well below 16 KiB even with base64 inflation and gzip headroom.
const MaxRequestBytes = 16 * 1024

// Watch stream bounds (ADR-0014). The lifetime forces clients to reconnect
// through the gateway session gate periodically; heartbeats keep proxies
// from idling out and carry the server watermark without advancing the
// durable cursor.
const (
	WatchMaxLifetime  = 2 * time.Minute
	WatchHeartbeat    = 15 * time.Second
	WatchPollInterval = 500 * time.Millisecond
	WatchMaxPerOwner  = 4
	WatchOwnerBudget  = 1024
	WatchChangesBatch = 100
)

// Handler serves the public NotificationService.
type Handler struct {
	service *application.Service
	// watchBudgets bounds concurrent streams per owner. It is an ephemeral
	// connection budget, not a durable authority; entries exist only while
	// streams are open.
	watchMu      sync.Mutex
	watchBudgets map[string]int
}

func New(service *application.Service) *Handler {
	return &Handler{service: service, watchBudgets: make(map[string]int)}
}

// NewConnectHandler wires the public transport with the pre-decode wire
// budget. Composition roots and tests must use this constructor.
func NewConnectHandler(service *application.Service) (string, http.Handler) {
	return notificationv1connect.NewNotificationServiceHandler(
		New(service),
		connect.WithReadMaxBytes(MaxRequestBytes),
	)
}

func (h *Handler) ListNotifications(ctx context.Context, req *connect.Request[notificationv1.ListNotificationsRequest]) (*connect.Response[notificationv1.ListNotificationsResponse], error) {
	owner, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	filter := ports.Filter{ProjectID: msg.GetProjectId(), UnreadOnly: msg.GetUnreadOnly()}
	if msg.Kind != nil {
		kind, err := kindString(msg.GetKind())
		if err != nil {
			return nil, err
		}
		filter.Kind = kind
	}
	page, next, err := h.service.List(ctx, owner, filter, int(msg.GetPageSize()), msg.GetPageToken())
	if err != nil {
		return nil, mapError(err)
	}
	notifications := make([]*notificationv1.Notification, 0, len(page.Notifications))
	for _, fact := range page.Notifications {
		notifications = append(notifications, notificationProto(fact))
	}
	return connect.NewResponse(&notificationv1.ListNotificationsResponse{
		Notifications: notifications,
		NextPageToken: next,
		UnreadCount:   page.UnreadCount,
		Watermark:     page.Watermark,
	}), nil
}

func (h *Handler) GetNotification(ctx context.Context, req *connect.Request[notificationv1.GetNotificationRequest]) (*connect.Response[notificationv1.GetNotificationResponse], error) {
	owner, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	fact, err := h.service.Get(ctx, owner, req.Msg.GetNotificationId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&notificationv1.GetNotificationResponse{Notification: notificationProto(fact)}), nil
}

func (h *Handler) GetNotificationSummary(ctx context.Context, req *connect.Request[notificationv1.GetNotificationSummaryRequest]) (*connect.Response[notificationv1.GetNotificationSummaryResponse], error) {
	owner, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := h.service.Summary(ctx, owner)
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&notificationv1.GetNotificationSummaryResponse{
		UnreadCount:         summary.UnreadCount,
		Watermark:           summary.Watermark,
		IncidentSourceReady: h.incidentSourceReady(),
	}), nil
}

func (h *Handler) MarkNotificationRead(ctx context.Context, req *connect.Request[notificationv1.MarkNotificationReadRequest]) (*connect.Response[notificationv1.MarkNotificationReadResponse], error) {
	owner, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.service.MarkRead(ctx, application.MarkReadInput{
		OwnerUserID:     owner,
		NotificationIDs: []string{req.Msg.GetNotificationId()},
		IdempotencyKey:  req.Msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	response := &notificationv1.MarkNotificationReadResponse{
		UnreadCount:    result.UnreadCount,
		ChangeSequence: result.ChangeSequence,
	}
	if len(result.Notifications) > 0 {
		response.Notification = notificationProto(result.Notifications[0])
	}
	return connect.NewResponse(response), nil
}

func (h *Handler) MarkNotificationsRead(ctx context.Context, req *connect.Request[notificationv1.MarkNotificationsReadRequest]) (*connect.Response[notificationv1.MarkNotificationsReadResponse], error) {
	owner, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	result, err := h.service.MarkRead(ctx, application.MarkReadInput{
		OwnerUserID:     owner,
		NotificationIDs: req.Msg.GetNotificationIds(),
		IdempotencyKey:  req.Msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&notificationv1.MarkNotificationsReadResponse{
		UnreadCount:    result.UnreadCount,
		ChangeSequence: result.ChangeSequence,
	}), nil
}

// WatchNotificationEvents is the resumable owner-wide server stream. Bounded
// lifetime, heartbeats, per-owner concurrency budget, and the swept-cursor
// RESET semantics all live here.
func (h *Handler) WatchNotificationEvents(ctx context.Context, req *connect.Request[notificationv1.WatchNotificationEventsRequest], stream *connect.ServerStream[notificationv1.WatchNotificationEventsResponse]) error {
	owner, err := requireOwner(ctx)
	if err != nil {
		return err
	}
	if req.Msg.GetAfterSequence() < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("watch cursor is invalid"))
	}
	release, err := h.acquireWatchBudget(owner)
	if err != nil {
		return err
	}
	defer release()

	cursor := req.Msg.GetAfterSequence()
	deadline := time.Now().Add(WatchMaxLifetime)
	heartbeat := time.NewTicker(WatchHeartbeat)
	defer heartbeat.Stop()
	poll := time.NewTicker(WatchPollInterval)
	defer poll.Stop()

	for {
		// Sweep-gap check first: a cursor inside or before the swept region
		// must be answered with an explicit reset, never a silent resume.
		swept, err := h.service.SweptThrough(ctx, owner)
		if err != nil {
			return mapError(err)
		}
		if cursor > 0 && cursor < swept {
			if err := stream.Send(&notificationv1.WatchNotificationEventsResponse{
				Payload: &notificationv1.WatchNotificationEventsResponse_ResetRequired{
					ResetRequired: &notificationv1.WatchResetRequired{SnapshotWatermark: swept},
				},
			}); err != nil {
				return mapError(err)
			}
			return nil
		}
		changes, err := h.service.Watch(ctx, owner, cursor, WatchChangesBatch)
		if err != nil {
			return mapError(err)
		}
		for _, change := range changes {
			if change.ChangeSequence <= cursor {
				continue
			}
			if err := stream.Send(&notificationv1.WatchNotificationEventsResponse{
				Payload: &notificationv1.WatchNotificationEventsResponse_Event{Event: changeProto(change)},
			}); err != nil {
				return mapError(err)
			}
			cursor = change.ChangeSequence
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			// Bounded lifetime: end the stream so the client reconnects
			// through the gateway session gate with its cursor.
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			watermark, err := h.service.Summary(ctx, owner)
			if err != nil {
				return mapError(err)
			}
			if err := stream.Send(&notificationv1.WatchNotificationEventsResponse{
				Payload: &notificationv1.WatchNotificationEventsResponse_Heartbeat{
					Heartbeat: &notificationv1.WatchHeartbeat{ServerWatermark: watermark.Watermark},
				},
			}); err != nil {
				return mapError(err)
			}
		case <-poll.C:
		}
	}
}

func (h *Handler) acquireWatchBudget(owner string) (func(), error) {
	h.watchMu.Lock()
	defer h.watchMu.Unlock()
	if len(h.watchBudgets) >= WatchOwnerBudget && h.watchBudgets[owner] == 0 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many notification streams"))
	}
	if h.watchBudgets[owner] >= WatchMaxPerOwner {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many notification streams for this owner"))
	}
	h.watchBudgets[owner]++
	released := false
	return func() {
		h.watchMu.Lock()
		defer h.watchMu.Unlock()
		if released {
			return
		}
		released = true
		h.watchBudgets[owner]--
		if h.watchBudgets[owner] <= 0 {
			delete(h.watchBudgets, owner)
		}
	}, nil
}

// incidentSourceReady is injected by the composition root; without the
// optional reliability consumer the incident source is honestly not ready.
var incidentSourceReadyFunc = func() bool { return false }

func (h *Handler) incidentSourceReady() bool { return incidentSourceReadyFunc() }

// SetIncidentSourceReady wires the incident-source freshness probe.
func SetIncidentSourceReady(probe func() bool) {
	if probe != nil {
		incidentSourceReadyFunc = probe
	}
}

func requireOwner(ctx context.Context) (string, error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("trusted identity is required"))
	}
	return id.UserID, nil
}

func kindString(kind notificationv1.NotificationKind) (string, error) {
	switch kind {
	case notificationv1.NotificationKind_NOTIFICATION_KIND_AGENT_APPROVAL_REQUIRED:
		return domain.KindAgentApprovalRequired, nil
	case notificationv1.NotificationKind_NOTIFICATION_KIND_AGENT_TASK_TERMINAL:
		return domain.KindAgentTaskTerminal, nil
	case notificationv1.NotificationKind_NOTIFICATION_KIND_ARTIFACT_REVIEW_CREATED:
		return domain.KindArtifactReviewCreated, nil
	case notificationv1.NotificationKind_NOTIFICATION_KIND_RELIABILITY_INCIDENT_OPENED:
		return domain.KindReliabilityIncidentOpen, nil
	case notificationv1.NotificationKind_NOTIFICATION_KIND_APP_INSTANCE_MESSAGE:
		return domain.KindAppInstanceMessage, nil
	}
	return "", connect.NewError(connect.CodeInvalidArgument, errors.New("notification kind is invalid"))
}

func kindEnum(kind string) notificationv1.NotificationKind {
	switch kind {
	case domain.KindAgentApprovalRequired:
		return notificationv1.NotificationKind_NOTIFICATION_KIND_AGENT_APPROVAL_REQUIRED
	case domain.KindAgentTaskTerminal:
		return notificationv1.NotificationKind_NOTIFICATION_KIND_AGENT_TASK_TERMINAL
	case domain.KindArtifactReviewCreated:
		return notificationv1.NotificationKind_NOTIFICATION_KIND_ARTIFACT_REVIEW_CREATED
	case domain.KindReliabilityIncidentOpen:
		return notificationv1.NotificationKind_NOTIFICATION_KIND_RELIABILITY_INCIDENT_OPENED
	case domain.KindAppInstanceMessage:
		return notificationv1.NotificationKind_NOTIFICATION_KIND_APP_INSTANCE_MESSAGE
	}
	return notificationv1.NotificationKind_NOTIFICATION_KIND_UNSPECIFIED
}

func severityEnum(severity string) notificationv1.NotificationSeverity {
	if severity == domain.SeverityCritical {
		return notificationv1.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL
	}
	return notificationv1.NotificationSeverity_NOTIFICATION_SEVERITY_NORMAL
}

func targetKindEnum(kind string) notificationv1.NotificationTargetKind {
	switch kind {
	case domain.TargetApproval:
		return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_APPROVAL
	case domain.TargetTask:
		return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_TASK
	case domain.TargetArtifact:
		return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_ARTIFACT
	case domain.TargetIncident:
		return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_INCIDENT
	case domain.TargetApp:
		return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_APP
	}
	return notificationv1.NotificationTargetKind_NOTIFICATION_TARGET_KIND_UNSPECIFIED
}

func notificationProto(fact domain.Notification) *notificationv1.Notification {
	wire := &notificationv1.Notification{
		Id:       fact.ID,
		Kind:     kindEnum(fact.Kind),
		Severity: severityEnum(fact.Severity),
		Origin:   notificationv1.NotificationOrigin_NOTIFICATION_ORIGIN_SYSTEM,
		Title:    fact.Title,
		Body:     fact.Body,
		Target: &notificationv1.NotificationTarget{
			Kind:     targetKindEnum(fact.TargetKind),
			TargetId: fact.TargetID,
			AppId:    fact.AppID,
		},
		CreatedAt: timestamppb.New(fact.CreatedAt),
		Revision:  fact.ReadChangeSequence,
	}
	if fact.ProjectID != "" {
		wire.ProjectId = fact.ProjectID
	}
	if fact.Origin == domain.OriginApp {
		wire.Origin = notificationv1.NotificationOrigin_NOTIFICATION_ORIGIN_APP
	}
	if !fact.ReadAt.IsZero() {
		wire.ReadAt = timestamppb.New(fact.ReadAt)
	}
	return wire
}

func changeProto(change domain.Change) *notificationv1.NotificationEvent {
	return &notificationv1.NotificationEvent{
		ChangeSequence: change.ChangeSequence,
		Type:           changeTypeEnum(change.ChangeType),
		NotificationId: change.NotificationID,
		Revision:       change.Revision,
		Notification:   notificationProto(change.Notification),
	}
}

func changeTypeEnum(changeType string) notificationv1.NotificationChangeType {
	if changeType == domain.ChangeRead {
		return notificationv1.NotificationChangeType_NOTIFICATION_CHANGE_TYPE_READ
	}
	return notificationv1.NotificationChangeType_NOTIFICATION_CHANGE_TYPE_CREATED
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, application.ErrInvalid), errors.Is(err, application.ErrTooMany), errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("notification request is invalid"))
	case errors.Is(err, application.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("notification request conflicts with a consumed key"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("notification is not available"))
	case errors.Is(err, ports.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("notification store is temporarily unavailable"))
	case errors.Is(err, domain.ErrCorrupt), errors.Is(err, ports.ErrSourceDigestDrift):
		return connect.NewError(connect.CodeInternal, errors.New("notification operation failed"))
	}
	return connect.NewError(connect.CodeInternal, errors.New("notification operation failed"))
}
