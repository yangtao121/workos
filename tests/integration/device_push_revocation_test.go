//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/core/notification/adapters/fixturerelay"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	notificationtransport "github.com/yangtao121/workos/internal/core/notification/transport"
	"github.com/yangtao121/workos/internal/gateway/auth/adapters/corepush"
	authpostgres "github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres"
	authapp "github.com/yangtao121/workos/internal/gateway/auth/application"
	authdomain "github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestDevicePushRevocation(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, device := integrationOwnerID, (ids.UUIDv7{}).New()
	key := newIntegrationKey(t)
	exec(`INSERT INTO workos_gateway.device_credentials
 (id, owner_user_id, name, device_class, public_key_spki, public_key_hash, revision, created_at, last_authenticated_at)
 VALUES ($1,$2,'Push device','desktop',$3,$4,1,now(),now())`, device, owner, key.spki, key.hash)
	exec(`INSERT INTO workos_core.users (id,kind,display_name,created_at) VALUES ($1,'owner','Push fixture',now())`, owner)
	gatewayStore := authpostgres.New(pool)
	auth := newIntegrationService(t, dsn, "sha256:"+strings.Repeat("a", 64))
	coreStore := notificationpostgres.New(pool)
	relay := fixturerelay.New()
	push, err := notificationapp.NewPushService(coreStore, map[string]notificationports.PushRelaySender{notificationdomain.PushPlatformFixture: relay}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deviceCtx := identity.WithContext(ctx, identity.Identity{UserID: owner, DeviceID: device})
	subscribe := func() error {
		return push.Subscribe(deviceCtx, device, notificationdomain.PushPlatformFixture, "fixture://revocation", "", "")
	}
	if err := subscribe(); err != nil {
		t.Fatal(err)
	}
	appendFact := func() {
		t.Helper()
		target := (ids.UUIDv7{}).New()
		fact, err := notificationdomain.PrepareSystemFact(notificationdomain.SystemFact{Kind: notificationdomain.KindAgentTaskTerminal, OwnerUserID: owner, TargetID: target, SourceID: target, Category: "completed"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, _, err := coreStore.AppendTx(ctx, tx, fact); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	appendFact()
	if err := push.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(relay.Delivered()) != 1 {
		t.Fatal("active device did not receive initial wake")
	}

	appendFact() // A pending wake must also be suppressed after revocation.
	op := authapp.RevokeDeviceInput{DeviceID: device, IdempotencyKey: (ids.UUIDv7{}).New(), ExpectedRevision: 1}
	actor := authdomain.SessionIdentity{OwnerID: owner}
	// Enqueue failure must abort the credential revocation itself.
	exec(`CREATE FUNCTION workos_gateway.reject_push_revocation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$;
 CREATE TRIGGER reject_push_revocation BEFORE INSERT ON workos_gateway.push_revocations FOR EACH ROW EXECUTE FUNCTION workos_gateway.reject_push_revocation()`)
	if _, _, err := auth.RevokeDevice(ctx, actor, op); err == nil {
		t.Fatal("revocation ignored outbox failure")
	}
	if _, err := gatewayStore.LoadActiveDevice(ctx, device); err != nil {
		t.Fatalf("outbox failure revoked credential: %v", err)
	}
	exec(`DROP TRIGGER reject_push_revocation ON workos_gateway.push_revocations; DROP FUNCTION workos_gateway.reject_push_revocation()`)
	if _, _, err := auth.RevokeDevice(ctx, authdomain.SessionIdentity{OwnerID: (ids.UUIDv7{}).New()}, op); !errors.Is(err, authdomain.ErrDeviceNotFound) {
		t.Fatalf("foreign revoke: %v", err)
	}
	if _, _, err := auth.RevokeDevice(ctx, actor, op); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := auth.RevokeDevice(ctx, actor, op); err != nil || !replayed {
		t.Fatalf("replay: %v %v", replayed, err)
	}

	path, handler := notificationtransport.NewDevicePushConnectHandler(push)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	var offline atomic.Bool
	offline.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	sink := corepush.New(server.Client(), server.URL)
	consumer := &authapp.PushRevocationConsumer{Store: gatewayStore, Sink: sink}
	makeDue := func() {
		exec(`UPDATE workos_gateway.push_revocations SET next_attempt_at=now()-interval '1 second' WHERE device_id=$1 AND delivered_at IS NULL`, device)
	}
	if err := consumer.Pass(ctx); err == nil {
		t.Fatal("offline Core reported synchronized")
	}
	claims, err := gatewayStore.ClaimPushRevocations(ctx, time.Now().UTC())
	if err != nil || len(claims) != 0 {
		t.Fatalf("unexpired lease stolen: %v %v", claims, err)
	}
	makeDue()
	old, err := gatewayStore.ClaimPushRevocations(ctx, time.Now().UTC())
	if err != nil || len(old) != 1 {
		t.Fatalf("claim recovery: %v %v", old, err)
	}
	makeDue()
	current, err := gatewayStore.ClaimPushRevocations(ctx, time.Now().UTC())
	if err != nil || len(current) != 1 {
		t.Fatal("second lease missing")
	}
	if err := gatewayStore.CompletePushRevocation(ctx, old[0], time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var delivered bool
	if err := pool.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM workos_gateway.push_revocations WHERE device_id=$1`, device).Scan(&delivered); err != nil || delivered {
		t.Fatalf("stale claim acknowledged new lease: %v", err)
	}

	offline.Store(false)
	// RPC succeeds, then the worker loses its acknowledgement. Late requests
	// racing this first consumption must never leave the device active.
	var wg sync.WaitGroup
	errs := make(chan error, 13)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- subscribe() }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); errs <- sink.RevokeDevicePush(ctx, current[0]) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, notificationdomain.ErrPushDenied) {
			t.Fatal(err)
		}
	}
	if err := subscribe(); !errors.Is(err, notificationdomain.ErrPushDenied) {
		t.Fatalf("late subscribe revived device: %v", err)
	}
	makeDue()
	consumer = &authapp.PushRevocationConsumer{Store: authpostgres.New(pool), Sink: corepush.New(server.Client(), server.URL)}
	if err := consumer.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT delivered_at IS NOT NULL FROM workos_gateway.push_revocations WHERE device_id=$1`, device).Scan(&delivered); err != nil || !delivered {
		t.Fatalf("replayed revocation unacknowledged: %v", err)
	}
	drift := current[0]
	drift.RevokedAt = drift.RevokedAt.Add(time.Second)
	if err := sink.RevokeDevicePush(ctx, drift); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("changed revocation replay accepted: %v", err)
	}
	otherDevice := (ids.UUIDv7{}).New()
	otherCtx := identity.WithContext(ctx, identity.Identity{UserID: owner, DeviceID: otherDevice})
	if err := push.Subscribe(otherCtx, otherDevice, notificationdomain.PushPlatformFixture, "fixture://other-device", "", ""); err != nil {
		t.Fatal(err)
	}
	appendFact()
	if err := push.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(relay.Delivered()) != 2 || relay.Delivered()[1].DeviceID != otherDevice {
		t.Fatal("revocation failed to isolate the target device")
	}
	if err := push.Unsubscribe(deviceCtx, device, notificationdomain.PushPlatformFixture); err != nil {
		t.Fatal(err)
	}
	if err := subscribe(); !errors.Is(err, notificationdomain.ErrPushDenied) {
		t.Fatal("platform unsubscribe erased permanent revocation")
	}
}
