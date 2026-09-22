package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"os"
	"testing"
	"time"
)

func lifecycleRepository(t *testing.T) *Repository {
	t.Helper()
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
	t.Cleanup(pool.Close)
	return New(pool)
}
func TestLifecyclePersistenceAndRestartReceipts(t *testing.T) {
	r := lifecycleRepository(t)
	ctx := context.Background()
	generator := ids.UUIDv7{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	owner, project := generator.New(), generator.New()
	var sessions []domain.Session
	for _, mode := range []domain.LifecycleMode{domain.LifecycleBounded, domain.LifecycleManualStop} {
		row := domain.Session{Generation: 1, LifecycleMode: mode, SessionID: generator.New(), OwnerUserID: owner, ProjectID: project, IdempotencyKey: generator.New(), RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: domain.StateQueued, CreatedAt: now, UpdatedAt: now, ExpiresAt: mode.Expiry(now)}
		if _, created, err := r.InsertSession(ctx, row); err != nil || !created {
			t.Fatalf("insert: %v %v", created, err)
		}
		if err := r.UpdateState(ctx, owner, row.SessionID, domain.StateRunning, now); err != nil {
			t.Fatal(err)
		}
		stored, err := r.GetSession(ctx, owner, row.SessionID)
		if err != nil || stored.LifecycleMode != mode || !stored.ExpiresAt.Equal(row.ExpiresAt) {
			t.Fatalf("policy roundtrip: %#v %v", stored, err)
		}
		sessions = append(sessions, stored)
	}
	expired, err := r.ExpireIdle(ctx, now.Add(time.Hour))
	if err != nil || len(expired) != 1 || expired[0] != sessions[0].SessionID {
		t.Fatalf("advanced-clock expiry: %v %v", expired, err)
	}
	manual, err := r.GetSession(ctx, owner, sessions[1].SessionID)
	if err != nil || manual.State != domain.StateRunning || !manual.ExpiresAt.IsZero() {
		t.Fatalf("manual-stop expired: %#v %v", manual, err)
	}
	key := generator.New()
	generation, fresh, err := r.BeginRestart(ctx, owner, manual.SessionID, key, now, domain.LifecycleBounded)
	if err != nil || !fresh || generation != 2 {
		t.Fatalf("bounded restart: %d %v %v", generation, fresh, err)
	}
	if _, fresh, err := r.BeginRestart(ctx, owner, manual.SessionID, key, now.Add(time.Minute), domain.LifecycleBounded); err != nil || fresh {
		t.Fatalf("replay: %v %v", fresh, err)
	}
	if _, _, err := r.BeginRestart(ctx, owner, manual.SessionID, key, now, domain.LifecycleManualStop); !errors.Is(err, domain.ErrIdempotencyDrift) {
		t.Fatalf("mode drift accepted: %v", err)
	}
	if _, _, err := r.BeginRestart(ctx, owner, manual.SessionID, generator.New(), now, domain.LifecycleManualStop); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginStop(ctx, owner, manual.SessionID, generator.New(), now); err != nil {
		t.Fatal(err)
	}
	stopped, err := r.GetSession(ctx, owner, manual.SessionID)
	if err != nil || !stopped.State.Terminal() || !stopped.ExpiresAt.IsZero() || stopped.LifecycleMode != domain.LifecycleManualStop {
		t.Fatalf("stop erased actual policy: %#v %v", stopped, err)
	}
	if _, err := r.GetSession(ctx, generator.New(), manual.SessionID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign owner resolved workload")
	}
}
