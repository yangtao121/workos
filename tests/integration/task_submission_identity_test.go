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
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestTaskSubmissionIdentityArbitratesConcurrentInputs(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Task fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	repo := agentpostgres.New(pool)
	type result struct {
		task agentdomain.Task
		err  error
	}
	results := make(chan result, 10)
	ready := make(chan struct{})
	for i := range 10 {
		go func() {
			<-ready
			now := time.Now().UTC()
			task := agentdomain.Task{ID: uuid.Must(uuid.NewV7()).String(), OwnerUserID: owner, Input: []byte(fmt.Sprintf(`{"role":"general","goal":"variant-%d"}`, i%2)), ProviderID: "fake", State: agentdomain.StateQueued, CreatedAt: now, UpdatedAt: now}
			saved, err := repo.Create(ctx, task, "repair-key")
			results <- result{saved, err}
		}()
	}
	close(ready)
	var winner agentdomain.Task
	successes, conflicts := 0, 0
	for range 10 {
		r := <-results
		switch {
		case r.err == nil:
			successes++
			if winner.ID != "" && winner.ID != r.task.ID {
				t.Fatal("one key created multiple tasks")
			}
			winner = r.task
		case errors.Is(r.err, agentdomain.ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("unexpected race result: %v", r.err)
		}
	}
	if successes != 5 || conflicts != 5 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var tasks, outbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.agent_tasks WHERE owner_user_id=$1`, owner).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_events.outbox WHERE aggregate_id=$1`, winner.ID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || outbox != 1 {
		t.Fatalf("task/outbox effects=%d/%d", tasks, outbox)
	}
	repo = agentpostgres.New(pool)
	replay := winner
	replay.ID = uuid.Must(uuid.NewV7()).String()
	replay.ProviderID = "changed-provider"
	saved, err := repo.Create(ctx, replay, "repair-key")
	if err != nil || saved.ID != winner.ID || saved.ProviderID != "fake" {
		t.Fatalf("replay lost original snapshot: task=%s provider=%s err=%v", saved.ID, saved.ProviderID, err)
	}
	replay.ProjectID = uuid.Must(uuid.NewV7()).String()
	if _, err := repo.Create(ctx, replay, "repair-key"); !errors.Is(err, agentdomain.ErrIdempotencyConflict) {
		t.Fatalf("changed project replayed: %v", err)
	}
}
