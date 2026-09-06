//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/notification/adapters/fixturerelay"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/notification/adapters/unavailable"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// Real notification transactions, not arbitrary ids, are the only source of wakes.
func TestPushRelay(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := notificationpostgres.New(pool)
	relay := fixturerelay.New()
	newService := func() *notificationapp.PushService {
		service, err := notificationapp.NewPushService(notificationpostgres.New(pool), map[string]ports.PushRelaySender{
			domain.PushPlatformFixture: relay, domain.PushPlatformWebPush: unavailable.New(),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newService()
	owner, deviceA, deviceB := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	foreign := uuid.Must(uuid.NewV7()).String()
	// This scratch database exercises the same multi-owner isolation as
	// notification_test; production retains the single-owner deployment policy.
	if _, err := pool.Exec(ctx, "DROP INDEX workos_core.users_single_owner_idx"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{owner, foreign} {
		if _, err := pool.Exec(ctx, "INSERT INTO workos_core.users (id,kind,display_name,created_at) VALUES ($1,'owner','Push fixture',now())", user); err != nil {
			t.Fatal(err)
		}
	}
	ownerCtx := identity.WithContext(ctx, identity.Identity{UserID: owner, DeviceID: deviceA})
	for _, device := range []string{deviceA, deviceB} {
		if err := service.Subscribe(ownerCtx, device, domain.PushPlatformFixture, "fixture://device/"+device, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Subscribe(ownerCtx, deviceA, domain.PushPlatformFixture, "", "", ""); err == nil {
		t.Fatal("empty endpoint accepted")
	}
	appendFact := func(fact domain.Notification, commit bool) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, _, err := repo.AppendTx(ctx, tx, fact); err != nil {
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	fact := func(ownerID string) domain.Notification {
		t.Helper()
		target := uuid.Must(uuid.NewV7()).String()
		n, err := domain.PrepareSystemFact(domain.SystemFact{Kind: domain.KindAgentTaskTerminal, OwnerUserID: ownerID, TargetID: target, SourceID: target, Category: "completed"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	pass := func() {
		t.Helper()
		if err := service.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		if got := len(relay.Delivered()); got != want {
			t.Fatalf("relay count %d, want %d", got, want)
		}
	}
	makeDue := func(id string) {
		t.Helper()
		if _, err := pool.Exec(ctx, "UPDATE workos_core.push_deliveries SET next_attempt_at=$2 WHERE notification_id=$1 AND state='pending'", id, time.Now().UTC().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	state := func(id, device string, want string) {
		t.Helper()
		var got string
		if err := pool.QueryRow(ctx, "SELECT state FROM workos_core.push_deliveries WHERE notification_id=$1 AND device_id=$2", id, device).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("state %s want %s", got, want)
		}
	}

	first := fact(owner)
	appendFact(first, false)
	pass()
	count(0) // rollback never wakes
	appendFact(first, true)
	relay.SetDown(true)
	pass()
	count(0)
	state(first.ID, deviceA, "pending")
	if delivered, err := repo.CountPushDeliveries(ctx, first.ID, deviceA, domain.PushPlatformFixture); err != nil || delivered != 0 {
		t.Fatalf("failed attempt counted as delivered: %d %v", delivered, err)
	}
	relay.SetDown(false)
	service = newService() // service recreation uses only persisted retry state
	makeDue(first.ID)
	pass()
	count(2)
	state(first.ID, deviceA, "delivered")
	appendFact(first, true)
	pass()
	count(2) // committed source replay does not enqueue again
	for _, record := range relay.Delivered() {
		var fields map[string]string
		if err := json.Unmarshal([]byte(record.PayloadJSON), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 1 || fields["notificationId"] != first.ID {
			t.Fatalf("non-whitelisted payload: %s", record.PayloadJSON)
		}
	}
	second := fact(owner)
	appendFact(second, true)
	for range 2 {
		if err := service.Unsubscribe(ownerCtx, deviceB, domain.PushPlatformFixture); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	count(3)
	state(second.ID, deviceB, "suppressed")

	quiet := domain.QuietHours{Enabled: true, Start: "00:00", End: "00:00"}
	if quiet, err = service.SetPreferences(ownerCtx, quiet); err != nil {
		t.Fatal(err)
	}
	third := fact(owner)
	appendFact(third, true)
	quiet.Enabled = false
	if quiet, err = service.SetPreferences(ownerCtx, quiet); err != nil {
		t.Fatal(err)
	}
	stale := quiet
	stale.Revision--
	stale.Enabled = true
	if _, err := service.SetPreferences(ownerCtx, stale); !errors.Is(err, domain.ErrPushConflict) {
		t.Fatalf("stale quiet preference overwrote latest: %v", err)
	}
	pass()
	count(3)
	state(third.ID, deviceA, "suppressed") // quiet-created wakes stay suppressed
	appendFact(fact(foreign), true)
	pass()
	count(3) // owner isolation

	fourth := fact(owner)
	appendFact(fourth, true)
	claims, err := repo.ClaimPushDeliveries(ctx, time.Now().UTC(), 8)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	parallel, err := notificationpostgres.New(pool).ClaimPushDeliveries(ctx, time.Now().UTC(), 8)
	if err != nil || len(parallel) != 0 {
		t.Fatalf("unexpired claim was stolen: %v %v", parallel, err)
	}
	makeDue(fourth.ID)
	replacement, err := repo.ClaimPushDeliveries(ctx, time.Now().UTC(), 8)
	if err != nil || len(replacement) != 1 {
		t.Fatalf("expired claim not recovered: %v %v", replacement, err)
	}
	if err := repo.CompletePushDelivery(ctx, claims[0], "delivered", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state(fourth.ID, deviceA, "pending") // stale worker cannot acknowledge new claim
	if err := repo.CompletePushDelivery(ctx, replacement[0], "pending", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	pass()
	count(4)
	state(fourth.ID, deviceA, "delivered")

	exhausted := fact(owner)
	appendFact(exhausted, true)
	relay.SetDown(true)
	for range 8 {
		makeDue(exhausted.ID)
		pass()
	}
	state(exhausted.ID, deviceA, "failed")
	relay.SetDown(false)
	pass()
	count(4)
}
