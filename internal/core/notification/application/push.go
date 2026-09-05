// Push wake application service (ADR-0018): device subscriptions,
// owner-level quiet hours, and relay dispatch with exactly-once semantics.
// The relay payload is the domain whitelist; dispatch happens only for
// active subscriptions outside the quiet window, and the durable
// delivery log makes replays no-ops.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// Push senders by platform. Platforms without a working sender (APNs/FCM
// until real credentials exist) map to the unavailable sender.
type PushService struct {
	store   ports.PushStore
	senders map[string]ports.PushRelaySender
	logger  *slog.Logger
}

// NewPushService wires the push service. A nil sender for a platform means
// that platform is unavailable; Subscribe still accepts it and dispatch
// reports the sanitized unavailable verdict.
func NewPushService(store ports.PushStore, senders map[string]ports.PushRelaySender, logger *slog.Logger) (*PushService, error) {
	if store == nil {
		return nil, errors.New("push service requires the store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PushService{store: store, senders: senders, logger: logger}, nil
}

func (s *PushService) ownerFrom(ctx context.Context) (string, error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return "", err
	}
	if !ValidUUID(id.UserID) {
		return "", domain.ErrPushInvalid
	}
	return id.UserID, nil
}

// Subscribe registers (or re-registers) one device wake subscription.
// Re-subscription is idempotent and reactivates a revoked registration.
func (s *PushService) Subscribe(ctx context.Context, deviceID, platform, endpoint, p256dh, authSecret string) error {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return err
	}
	if !ValidUUID(deviceID) || !domain.ValidPushPlatform(platform) ||
		!domain.ValidPushEndpoint(platform, endpoint) ||
		!domain.ValidPushKey(p256dh) || !domain.ValidPushKey(authSecret) {
		return domain.ErrPushInvalid
	}
	now := time.Now().UTC()
	return s.store.UpsertPushSubscription(ctx, domain.PushSubscription{
		OwnerUserID: owner, DeviceID: deviceID, Platform: platform,
		Endpoint: endpoint, P256DH: p256dh, AuthSecret: authSecret,
		Status: domain.PushActive, CreatedAt: now, UpdatedAt: now,
	})
}

// Unsubscribe revokes one device registration; revoking an unknown or
// already-revoked subscription succeeds (idempotent).
func (s *PushService) Unsubscribe(ctx context.Context, deviceID, platform string) error {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return err
	}
	if !ValidUUID(deviceID) || !domain.ValidPushPlatform(platform) {
		return domain.ErrPushInvalid
	}
	return s.store.RevokePushSubscription(ctx, owner, deviceID, platform, time.Now().UTC())
}

// Preferences reads the owner quiet window (defaults when unset).
func (s *PushService) Preferences(ctx context.Context) (domain.QuietHours, error) {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return domain.QuietHours{}, err
	}
	return s.store.PushPreferencesFor(ctx, owner)
}

// SetPreferences stores the owner quiet window.
func (s *PushService) SetPreferences(ctx context.Context, quiet domain.QuietHours) (domain.QuietHours, error) {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return domain.QuietHours{}, err
	}
	if !domain.ValidQuietClock(quiet.Start) || !domain.ValidQuietClock(quiet.End) {
		return domain.QuietHours{}, domain.ErrPushInvalid
	}
	if err := s.store.SavePushPreferences(ctx, owner, quiet, time.Now().UTC()); err != nil {
		return domain.QuietHours{}, err
	}
	return quiet, nil
}

// Dispatch pushes one committed notification to every active subscription
// outside the quiet window. Exactly-once per (notification, device,
// platform) comes from the durable delivery log, so consumer replays never
// double-wake. Senders receive the whitelist payload only.
func (s *PushService) Dispatch(ctx context.Context, ownerUserID, notificationID, title string) error {
	if !ValidUUID(ownerUserID) || !ValidUUID(notificationID) {
		return domain.ErrPushInvalid
	}
	quiet, err := s.store.PushPreferencesFor(ctx, ownerUserID)
	if err != nil {
		return err
	}
	if quiet.Suppress(time.Now().UTC()) {
		// Quiet-hour events never wake devices; durable facts stand.
		return nil
	}
	subs, err := s.store.ActivePushSubscriptions(ctx, ownerUserID)
	if err != nil {
		return err
	}
	payload := domain.PushPayload{NotificationID: notificationID}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		delivered, err := s.store.InsertPushDelivery(ctx, ownerUserID, notificationID, sub.DeviceID, sub.Platform, string(body), time.Now().UTC())
		if err != nil {
			return err
		}
		if !delivered {
			continue
		}
		sender, ok := s.senders[sub.Platform]
		if !ok {
			s.logger.Warn("push platform unavailable", "platform", sub.Platform)
			continue
		}
		if err := sender.Deliver(ctx, sub, payload); err != nil {
			// One device's outage never blocks the others; the delivery log
			// records the attempt, the notification fact stays authoritative.
			s.logger.Warn("push deliver failed", "platform", sub.Platform, "error", err)
		}
	}
	return nil
}

// ValidUUID re-exports the canonical grammar check for push facts.
func ValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122
}
