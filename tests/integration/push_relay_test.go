//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	fixturerelay "github.com/yangtao121/workos/internal/core/notification/adapters/fixturerelay"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	unavailable "github.com/yangtao121/workos/internal/core/notification/adapters/unavailable"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// pushQuietClock renders a UTC "HH:MM" offset from now.
func pushQuietClock(offset time.Duration) string {
	t := time.Now().UTC().Add(offset)
	return t.Format("15:04")
}

// TestPushRelay proves the ADR-0018 push slice over the real Core store:
// the relay payload whitelist (notification id and nothing else),
// exactly-once dispatch under at-least-once replay, idempotent revocation,
// the owner quiet window verdict, and honest unavailable platforms.
func TestPushRelay(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	relay := fixturerelay.New()
	pushService, err := notificationapp.NewPushService(
		notificationpostgres.New(pool),
		map[string]notificationports.PushRelaySender{
			notificationdomain.PushPlatformFixture: relay,
			notificationdomain.PushPlatformWebPush: unavailable.New(),
		},
		slog.Default(),
	)
	if err != nil {
		t.Fatal(err)
	}

	owner := "01999999-9999-7999-8999-000000000e01"
	deviceA := "01999999-9999-7999-8999-000000000ea1"
	deviceB := "01999999-9999-7999-8999-000000000ea2"
	deviceC := "01999999-9999-7999-8999-000000000ea3"
	ownerCtx := identity.WithContext(ctx, identity.Identity{UserID: owner, DeviceID: deviceA})

	// Subscriptions are owner-scoped, idempotent, and validated.
	if err := pushService.Subscribe(ownerCtx, deviceA, notificationdomain.PushPlatformFixture, "fixture://relay/device-a", "", ""); err != nil {
		t.Fatalf("subscribe deviceA: %v", err)
	}
	if err := pushService.Subscribe(ownerCtx, deviceB, notificationdomain.PushPlatformFixture, "fixture://relay/device-b", "", ""); err != nil {
		t.Fatalf("subscribe deviceB: %v", err)
	}
	// The web-push platform subscribes but its sender is honestly
	// unavailable (RFC 8291 encryption is a later scope).
	if err := pushService.Subscribe(ownerCtx, deviceC, notificationdomain.PushPlatformWebPush, "https://push.example/sub/c", "p256dh-key", "auth-secret"); err != nil {
		t.Fatalf("subscribe deviceC: %v", err)
	}
	if err := pushService.Subscribe(ownerCtx, deviceA, notificationdomain.PushPlatformFixture, "", "", ""); err == nil {
		t.Fatal("empty endpoint must fail closed")
	}

	// First dispatch wakes both fixture devices. The recorded payload must
	// contain exactly the whitelisted key: no title, body, project, or any
	// other field may leak through the relay.
	notificationID := "01999999-9999-7999-8999-000000000eb1"
	richTitle := "critical incident with secret body content"
	if err := pushService.Dispatch(ctx, owner, notificationID, richTitle); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	delivered := relay.Delivered()
	if len(delivered) != 2 {
		t.Fatalf("relay deliveries = %d, want 2 fixture devices", len(delivered))
	}
	for _, record := range delivered {
		var fields map[string]any
		if err := json.Unmarshal([]byte(record.PayloadJSON), &fields); err != nil {
			t.Fatalf("relay payload is not JSON: %v", record.PayloadJSON)
		}
		if len(fields) != 1 {
			t.Fatalf("relay payload whitelist violated: %s", record.PayloadJSON)
		}
		if _, ok := fields["notificationId"]; !ok {
			t.Fatalf("relay payload missing notificationId: %s", record.PayloadJSON)
		}
	}
	for _, record := range delivered {
		if strings.Contains(record.PayloadJSON, "secret body content") || strings.Contains(record.PayloadJSON, richTitle) {
			t.Fatalf("relay payload leaked content: %s", record.PayloadJSON)
		}
	}

	// At-least-once replay: a second dispatch of the same notification is a
	// no-op on the relay (exactly-once wake), regardless of device count.
	if err := pushService.Dispatch(ctx, owner, notificationID, richTitle); err != nil {
		t.Fatalf("replayed dispatch: %v", err)
	}
	if got := len(relay.Delivered()); got != 2 {
		t.Fatalf("replayed dispatch changed relay count to %d", got)
	}

	// Revocation is idempotent and stops delivery for that device only.
	if err := pushService.Unsubscribe(ownerCtx, deviceB, notificationdomain.PushPlatformFixture); err != nil {
		t.Fatalf("unsubscribe deviceB: %v", err)
	}
	if err := pushService.Unsubscribe(ownerCtx, deviceB, notificationdomain.PushPlatformFixture); err != nil {
		t.Fatalf("idempotent unsubscribe: %v", err)
	}
	second := "01999999-9999-7999-8999-000000000eb2"
	if err := pushService.Dispatch(ctx, owner, second, "second"); err != nil {
		t.Fatalf("dispatch after revoke: %v", err)
	}
	delivered = relay.Delivered()
	if len(delivered) != 3 {
		t.Fatalf("relay deliveries after revoke = %d, want 3", len(delivered))
	}
	for _, record := range delivered {
		if record.NotificationID == second && record.DeviceID == deviceB {
			t.Fatal("revoked device was woken")
		}
	}

	// The quiet window suppresses wakes server-side; the durable facts and
	// preferences are unaffected.
	quiet := notificationdomain.QuietHours{
		Enabled: true,
		Start:   pushQuietClock(-5 * time.Minute),
		End:     pushQuietClock(5 * time.Minute),
	}
	if _, err := pushService.SetPreferences(ownerCtx, quiet); err != nil {
		t.Fatalf("set preferences: %v", err)
	}
	stored, err := pushService.Preferences(ownerCtx)
	if err != nil || !stored.Enabled {
		t.Fatalf("preferences roundtrip drifted: %+v err=%v", stored, err)
	}
	third := "01999999-9999-7999-8999-000000000eb3"
	if err := pushService.Dispatch(ctx, owner, third, "quiet event"); err != nil {
		t.Fatalf("quiet dispatch: %v", err)
	}
	if got := len(relay.Delivered()); got != 3 {
		t.Fatalf("quiet window did not suppress wakes: relay = %d", got)
	}

	// Disabling quiet resumes delivery.
	quiet.Enabled = false
	if _, err := pushService.SetPreferences(ownerCtx, quiet); err != nil {
		t.Fatalf("disable quiet: %v", err)
	}
	if err := pushService.Dispatch(ctx, owner, third, "re-wake"); err != nil {
		t.Fatalf("post-quiet dispatch: %v", err)
	}
	if got := len(relay.Delivered()); got != 4 {
		t.Fatalf("post-quiet relay = %d, want 4", got)
	}

	// A relay outage is observable and never corrupts state: dispatch
	// succeeds overall, the attempt is logged, and nothing pretends.
	relay.SetDown(true)
	fourth := "01999999-9999-7999-8999-000000000eb4"
	if err := pushService.Dispatch(ctx, owner, fourth, "outage"); err != nil {
		t.Fatalf("outage dispatch must not fail the caller: %v", err)
	}
	if got := len(relay.Delivered()); got != 4 {
		t.Fatalf("down relay delivered %d", got)
	}
	relay.SetDown(false)

	// The quiet-window math: overnight wrap and daylight-agnostic UTC.
	window := notificationdomain.QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	for _, probe := range []struct {
		hour int
		want bool
	}{
		{hour: 23, want: true}, {hour: 3, want: true}, {hour: 6, want: true},
		{hour: 7, want: false}, {hour: 12, want: false}, {hour: 21, want: false},
	} {
		stamp := time.Date(2026, 9, 5, probe.hour, 30, 0, 0, time.UTC)
		if got := window.Suppress(stamp); got != probe.want {
			t.Fatalf("Suppress(%02d:30) = %v, want %v", probe.hour, got, probe.want)
		}
	}
	always := notificationdomain.QuietHours{Enabled: true, Start: "09:00", End: "09:00"}
	if !always.Suppress(time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)) {
		t.Fatal("equal bounds must mean always quiet")
	}
	if (notificationdomain.QuietHours{Enabled: false, Start: "00:00", End: "23:59"}).Suppress(time.Now()) {
		t.Fatal("disabled window must never suppress")
	}

	// Foreign owners never see each other's dispatches.
	foreign := "01999999-9999-7999-8999-000000000e02"
	if err := pushService.Dispatch(ctx, foreign, third, "foreign"); err != nil {
		t.Fatalf("foreign dispatch: %v", err)
	}
	if got := len(relay.Delivered()); got != 4 {
		t.Fatalf("foreign owner woke %d devices", got-4)
	}
}
