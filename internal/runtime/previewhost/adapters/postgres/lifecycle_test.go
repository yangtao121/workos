package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"os"
	"testing"
	"time"
)

func TestPreviewLifecyclePersistence(t *testing.T) {
	url := os.Getenv("WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires WORKOS_LIFECYCLE_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	if err := migrations.Run(ctx, url); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	r := New(pool)
	generator := ids.UUIDv7{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	row := ports.PreviewRecord{PreviewID: generator.New(), OwnerUserID: generator.New(), ProjectID: generator.New(), IdempotencyKey: generator.New(), WorkspaceSourceID: generator.New(), BindingID: generator.New(), BindingRevision: 1, State: domain.StateQueued, CreatedAt: now, UpdatedAt: now, Command: "fixture", Port: 3000, Generation: 1, AccessToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LifecycleMode: domain.LifecycleManualStop}
	if ok, err := r.Insert(ctx, row); err != nil || !ok {
		t.Fatalf("insert: %v %v", ok, err)
	}
	if err := r.Activate(ctx, row); err != nil {
		t.Fatal(err)
	}
	row, err = r.Get(ctx, row.PreviewID)
	if err != nil || !row.Live(now.Add(time.Hour)) || !row.ExpiresAt.IsZero() || row.LifecycleMode != domain.LifecycleManualStop {
		t.Fatalf("manual-stop roundtrip: %#v %v", row, err)
	}
	key := generator.New()
	restarted, fresh, err := r.Action(ctx, row.OwnerUserID, row.PreviewID, key, "restart", now, domain.LifecycleBounded)
	if err != nil || !fresh || restarted.LifecycleMode != domain.LifecycleBounded || !restarted.ExpiresAt.Equal(now.Add(domain.PreviewTTL)) {
		t.Fatalf("restart: %#v %v %v", restarted, fresh, err)
	}
	if _, fresh, err = r.Action(ctx, row.OwnerUserID, row.PreviewID, key, "restart", now, domain.LifecycleBounded); err != nil || fresh {
		t.Fatalf("replay: %v %v", fresh, err)
	}
	if _, _, err = r.Action(ctx, row.OwnerUserID, row.PreviewID, key, "restart", now, domain.LifecycleManualStop); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("restart mode drift: %v", err)
	}
	if _, _, err = r.Action(ctx, row.OwnerUserID, row.PreviewID, generator.New(), "restart", now, domain.LifecycleManualStop); err != nil {
		t.Fatal(err)
	}
	stopped, _, err := r.Action(ctx, row.OwnerUserID, row.PreviewID, generator.New(), "stop", now)
	if err != nil || stopped.State != domain.StateStopped || stopped.LifecycleMode != domain.LifecycleManualStop || !stopped.ExpiresAt.IsZero() {
		t.Fatalf("stop: %#v %v", stopped, err)
	}
}
