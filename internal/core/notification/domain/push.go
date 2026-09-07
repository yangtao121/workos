// Push wake facts (ADR-0018): device subscriptions, the owner-level quiet
// window, and the relay payload whitelist. The relay learns the notification
// id and nothing else — body content, project names, code, and Agent output
// never leave Core through the push path.
package domain

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"time"
)

var (
	ErrPushDenied = errors.New("push device is not authorized")
	// ErrPushInvalid rejects malformed subscriptions and preferences.
	ErrPushInvalid = errors.New("push subscription or preference is invalid")
	// ErrPushUnavailable reports a delivery path without a working sender
	// (APNs/FCM until real credentials exist).
	ErrPushUnavailable = errors.New("push delivery is not available for this platform")
	ErrPushExpired     = errors.New("push subscription has expired")
	ErrPushConflict    = errors.New("push preferences changed")
)

// Push platforms. Real APNs/FCM require external provider accounts; until
// they exist the senders report ErrPushUnavailable instead of pretending.
const (
	PushPlatformWebPush = "web-push"
	PushPlatformFixture = "fixture"
)

// PushSubscriptionStatus is the subscription lifecycle. Revocation is
// idempotent and stops dispatch.
const (
	PushActive  = "active"
	PushRevoked = "revoked"
)

var quietClockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidPushPlatform pins the platform vocabulary.
func ValidPushPlatform(platform string) bool {
	return platform == PushPlatformWebPush || platform == PushPlatformFixture
}

// ValidPushEndpoint pins the endpoint grammar: an absolute https URL for the
// web-push platform, any bounded opaque reference for the fixture relay.
func ValidPushEndpoint(platform, endpoint string) bool {
	if endpoint == "" || len(endpoint) > 2048 {
		return false
	}
	if platform == PushPlatformWebPush {
		parsed, err := url.Parse(endpoint)
		return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
	}
	return true
}

// ValidPushKey bounds the Web Push client key material grammar (base64url
// blobs are checked loosely here; the encrypted payload is the boundary).
func ValidPushKey(value string) bool { return len(value) <= 512 }

func ValidWebPushKeys(public, auth string) bool {
	key, err := base64.RawURLEncoding.Strict().DecodeString(public)
	if err != nil {
		return false
	}
	if _, err := ecdh.P256().NewPublicKey(key); err != nil {
		return false
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(auth)
	return err == nil && len(secret) == 16
}

// PushSubscription is one device wake registration.
type PushSubscription struct {
	OwnerUserID string
	DeviceID    string
	Platform    string
	Endpoint    string
	P256DH      string
	AuthSecret  string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// QuietHours is the owner-level do-not-disturb window in UTC.
type QuietHours struct {
	Revision int64
	Enabled  bool
	Start    string // "HH:MM" UTC
	End      string // "HH:MM" UTC
}

// ValidQuietClock pins the "HH:MM" grammar.
func ValidQuietClock(value string) bool { return quietClockPattern.MatchString(value) }

// Suppress reports whether a UTC instant falls inside the quiet window.
// Overnight windows (start >= end) wrap past midnight; equal bounds mean an
// always-quiet window. Quiet-hour events never send relay wakes; durable
// notification facts are unaffected either way.
func (q QuietHours) Suppress(now time.Time) bool {
	if !q.Enabled || !ValidQuietClock(q.Start) || !ValidQuietClock(q.End) {
		return false
	}
	minutes := now.UTC().Hour()*60 + now.UTC().Minute()
	start := parseClock(q.Start)
	end := parseClock(q.End)
	if start == end {
		return true
	}
	if start < end {
		return minutes >= start && minutes < end
	}
	return minutes >= start || minutes < end
}

func parseClock(value string) int {
	return int(value[0]-'0')*600 + int(value[1]-'0')*60 + int(value[3]-'0')*10 + int(value[4]-'0')
}

// PushPayload is the complete whitelist of relay-visible fields. The fixture
// relay records exactly this JSON; any future field must be added here (and
// to the whitelist test) deliberately.
type PushPayload struct {
	NotificationID string `json:"notificationId"`
}

// PushDelivery is a leased outbox entry. A relay can accept a wake before a
// process loses its lease; consumers must deduplicate by notification id.
type PushDelivery struct {
	Subscription   PushSubscription
	NotificationID string
	ClaimToken     string
	Attempts       int32
}
