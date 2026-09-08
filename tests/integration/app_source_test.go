//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	registrydb "github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres/appdb"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestAppSourceConcurrentIdentityAndStoredIntegrity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	owner := uuid.Must(uuid.NewV7()).String()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Source fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	service, err := registryapp.NewSourceService(registrydb.New(pool), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		bundle domain.SourceBundle
		err    error
	}
	ready := make(chan struct{})
	results := make(chan result, 10)
	for i := range 10 {
		go func() {
			<-ready
			bundle, err := service.Create(ctx, owner, "source-key", []domain.SourceFile{{Path: "src/main.go", Content: []byte(fmt.Sprintf("variant-%d", i%2))}, {Path: "README.md", Content: []byte("fixture")}})
			results <- result{bundle, err}
		}()
	}
	close(ready)
	var winner domain.SourceBundle
	accepted, conflicted := 0, 0
	for range 10 {
		result := <-results
		if errors.Is(result.err, domain.ErrIdempotencyConflict) {
			conflicted++
			continue
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		accepted++
		if winner.ID != "" && (result.bundle.ID != winner.ID || result.bundle.Digest != winner.Digest || !result.bundle.CreatedAt.Equal(winner.CreatedAt)) {
			t.Fatal("concurrent request drifted")
		}
		winner = result.bundle
	}
	if accepted != 5 || conflicted != 5 {
		t.Fatalf("accepted=%d conflicted=%d", accepted, conflicted)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.app_source_bundles WHERE owner_user_id=$1`, owner).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source count=%d err=%v", count, err)
	}
	service, err = registryapp.NewSourceService(registrydb.New(pool), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	reversed := []domain.SourceFile{winner.Files[1], winner.Files[0]}
	replay, err := service.Create(ctx, owner, "source-key", reversed)
	if err != nil || replay.ID != winner.ID || replay.Digest != winner.Digest || !replay.CreatedAt.Equal(winner.CreatedAt) {
		t.Fatalf("restart replay drifted: %v", err)
	}
	if _, err := service.Get(ctx, uuid.Must(uuid.NewV7()).String(), winner.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign source read: %v", err)
	}
	reversed[0].Executable = !reversed[0].Executable
	if _, err := service.Create(ctx, owner, "source-key", reversed); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("changed executable bit replayed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workos_core.app_source_bundles SET total_size_bytes=total_size_bytes+1 WHERE id=$1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, owner, winner.ID); !errors.Is(err, domain.ErrSourceCorrupt) {
		t.Fatalf("stored corruption hidden: %v", err)
	}
	if _, err := service.Create(ctx, owner, "source-key", winner.Files); !errors.Is(err, domain.ErrSourceCorrupt) {
		t.Fatalf("corrupt source overwritten on replay: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workos_core.app_source_bundles SET files=jsonb_build_array(jsonb_build_object('path','file','content',repeat('A',2097152),'executable',false)) WHERE id=$1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	bounded, err := appdb.New(pool).GetAppSourceBundle(ctx, appdb.GetAppSourceBundleParams{OwnerUserID: owner, ID: winner.ID})
	if err != nil || len(bounded.Files) != 0 {
		t.Fatalf("oversized stored body crossed the SQL read boundary: size=%d err=%v", len(bounded.Files), err)
	}
	if _, err := service.Get(ctx, owner, winner.ID); !errors.Is(err, domain.ErrSourceCorrupt) {
		t.Fatalf("oversized corruption hidden: %v", err)
	}

}
