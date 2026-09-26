package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres"
	"github.com/yangtao121/workos/internal/gateway/auth/adapters/randsource"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/platform/database"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// Run with an isolated empty PostgreSQL database. This exercises the actual
// migration, row locking, revocation, and mode-specific session facts.
func TestPasswordAuthPostgres(t *testing.T) {
	dsn := os.Getenv("WORKOS_PASSWORD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set WORKOS_PASSWORD_TEST_DATABASE_URL to an isolated empty PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	owner := "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	app, err := application.NewPasswordService(postgres.New(pool), owner, time.Hour, randsource.Clock{}, randsource.Entropy{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SetPassword(ctx, "owner", "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM workos_gateway.password_credentials WHERE owner_user_id=$1`, owner).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "$argon2id$v=19$") || strings.Contains(stored, "sufficiently") {
		t.Fatal("password not stored as Argon2id hash")
	}
	if _, err := app.Login(ctx, "owner", "wrong-password", "Mac Chrome", "desktop"); !errors.Is(err, domain.ErrAuthenticationFailed) {
		t.Fatalf("wrong password: %v", err)
	}
	first, err := app.Login(ctx, "owner", "a sufficiently long password", "Mac Chrome", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Login(ctx, "owner", "a sufficiently long password", "Other Chrome", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if first.Device.ID == second.Device.ID {
		t.Fatal("separate logins reused a device id")
	}
	identity, err := app.ResolveSession(ctx, first.SessionToken)
	if err != nil || identity.DeviceID != first.Device.ID {
		t.Fatalf("resolve first session: %v", err)
	}
	devices, _, err := app.ListDevices(ctx, identity, 10, "")
	if err != nil || len(devices) != 2 {
		t.Fatalf("list devices: count=%d err=%v", len(devices), err)
	}
	revoke := application.RevokeDeviceInput{DeviceID: second.Device.ID, ExpectedRevision: 1, IdempotencyKey: ids.UUIDv7{}.New()}
	if _, replayed, err := app.RevokeDevice(ctx, identity, revoke); err != nil || replayed {
		t.Fatalf("revoke: replayed=%t err=%v", replayed, err)
	}
	if _, replayed, err := app.RevokeDevice(ctx, identity, revoke); err != nil || !replayed {
		t.Fatalf("replay: replayed=%t err=%v", replayed, err)
	}
	if _, err := app.ResolveSession(ctx, second.SessionToken); !errors.Is(err, domain.ErrAuthenticationFailed) {
		t.Fatalf("revoked session: %v", err)
	}
	if err := app.SetPassword(ctx, "owner", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ResolveSession(ctx, first.SessionToken); !errors.Is(err, domain.ErrAuthenticationFailed) {
		t.Fatalf("rotated session: %v", err)
	}
	if _, err := app.Login(ctx, "owner", "a sufficiently long password", "Mac Chrome", "desktop"); !errors.Is(err, domain.ErrAuthenticationFailed) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := app.Login(ctx, "owner", "another long password", "Mac Chrome", "desktop"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}
