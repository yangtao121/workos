// Push wake application service (ADR-0018): device subscriptions,
// owner-level quiet hours and durable at-least-once relay delivery.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	id, identityErr := identity.FromContext(ctx)
	if identityErr != nil || id.DeviceID != deviceID {
		return domain.ErrPushDenied
	}
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return err
	}
	if !ValidUUID(deviceID) || !domain.ValidPushPlatform(platform) ||
		!domain.ValidPushEndpoint(platform, endpoint) ||
		!domain.ValidPushKey(p256dh) || !domain.ValidPushKey(authSecret) {
		return domain.ErrPushInvalid
	}
	if platform == domain.PushPlatformWebPush {
		if !domain.ValidWebPushKeys(p256dh, authSecret) {
			return domain.ErrPushInvalid
		}
		if s.PublicKey() == "" {
			return domain.ErrPushUnavailable
		}
	}
	now := time.Now().UTC()
	return s.store.UpsertPushSubscription(ctx, domain.PushSubscription{
		OwnerUserID: owner, DeviceID: deviceID, Platform: platform,
		Endpoint: endpoint, P256DH: p256dh, AuthSecret: authSecret,
		Status: domain.PushActive, CreatedAt: now, UpdatedAt: now,
	})
}

// Only the public subscription key crosses the transport boundary.
func (s *PushService) PublicKey() string {
	if sender, ok := s.senders[domain.PushPlatformWebPush].(interface{ PublicKey() string }); ok {
		return sender.PublicKey()
	}
	return ""
}

// Unsubscribe revokes one device registration; revoking an unknown or
// already-revoked subscription succeeds (idempotent).
func (s *PushService) Unsubscribe(ctx context.Context, deviceID, platform string) error {
	id, identityErr := identity.FromContext(ctx)
	if identityErr != nil || id.DeviceID != deviceID {
		return domain.ErrPushDenied
	}
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return err
	}
	if !ValidUUID(deviceID) || !domain.ValidPushPlatform(platform) {
		return domain.ErrPushInvalid
	}
	return s.store.RevokePushSubscription(ctx, owner, deviceID, platform, time.Now().UTC())
}

// RevokeDevice consumes a Gateway revocation; unlike a platform unsubscribe,
// this permanently disables the device UUID and serializes with late subscribes.
func (s *PushService) RevokeDevice(ctx context.Context, revokedAt time.Time) error {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return err
	}
	id, err := identity.FromContext(ctx)
	if err != nil || !ValidUUID(id.DeviceID) || revokedAt.IsZero() {
		return domain.ErrPushInvalid
	}
	return s.store.RevokePushDevice(ctx, owner, id.DeviceID, revokedAt.UTC())
}

// SubscriptionDigest returns only the authenticated device's active endpoint digest.
func (s *PushService) SubscriptionDigest(ctx context.Context) (string, error) {
	owner, err := s.ownerFrom(ctx)
	if err != nil {
		return "", err
	}
	id, err := identity.FromContext(ctx)
	if err != nil || !ValidUUID(id.DeviceID) {
		return "", domain.ErrPushDenied
	}
	sub, err := s.store.PushSubscriptionFor(ctx, owner, id.DeviceID, domain.PushPlatformWebPush)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sub.Status != domain.PushActive {
		return "", nil
	}
	hash := sha256.Sum256([]byte(sub.Endpoint))
	return "sha256:" + hex.EncodeToString(hash[:]), nil
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
	if quiet.Revision < 0 || !domain.ValidQuietClock(quiet.Start) || !domain.ValidQuietClock(quiet.End) {
		return domain.QuietHours{}, domain.ErrPushInvalid
	}
	return s.store.SavePushPreferences(ctx, owner, quiet, time.Now().UTC())
}

// Pass delivers a bounded leased batch. Success is recorded only after the
// relay accepts the wake. A lost acknowledgement can produce a duplicate;
// notification id is the recipient's idempotency key.
func (s *PushService) Pass(ctx context.Context) error {
	deliveries, err := s.store.ClaimPushDeliveries(ctx, time.Now().UTC(), 8)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		sub, err := s.store.PushSubscriptionFor(ctx, delivery.Subscription.OwnerUserID, delivery.Subscription.DeviceID, delivery.Subscription.Platform)
		if err != nil {
			return err
		}
		quiet, err := s.store.PushPreferencesFor(ctx, sub.OwnerUserID)
		if err != nil {
			return err
		}
		state := "suppressed"
		next := time.Now().UTC()
		if sub.Status == domain.PushActive && !quiet.Suppress(next) {
			state = "pending"
			sender := s.senders[sub.Platform]
			sendErr := domain.ErrPushUnavailable
			if sender != nil {
				sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				sendErr = sender.Deliver(sendCtx, sub, domain.PushPayload{NotificationID: delivery.NotificationID})
				cancel()
			}
			if errors.Is(sendErr, domain.ErrPushExpired) {
				if err := s.store.RevokePushSubscription(ctx, sub.OwnerUserID, sub.DeviceID, sub.Platform, time.Now().UTC()); err != nil {
					return err
				}
				state = "suppressed"
			} else if sendErr == nil {
				state = "delivered"
			} else {
				// Egress errors may contain subscription URLs or credentials.
				s.logger.Warn("push delivery deferred", "platform", sub.Platform, "attempt", delivery.Attempts)
				next = next.Add(time.Duration(1<<delivery.Attempts) * time.Second)
				if delivery.Attempts >= 8 {
					state = "failed"
				}
			}
		}
		if err := s.store.CompletePushDelivery(ctx, delivery, state, next); err != nil {
			return err
		}
	}
	return nil
}

func (s *PushService) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("push outbox pass failed")
			}
		}
	}
}

// ValidUUID re-exports the canonical grammar check for push facts.
func ValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122 && parsed.String() == value
}
