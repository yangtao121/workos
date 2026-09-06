//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/migrations"
	reliabilitypostgres "github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	reliabilityapp "github.com/yangtao121/workos/internal/reliability/application"
)

func TestDeploymentReconciliationDurability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
	repo, err := reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	candidate := reliabilityapp.DeploymentCandidate{IncidentID: id(), OwnerUserID: id(), ProjectID: id(), InstallationID: id(), TargetVersion: "2.0.0", ExpectedRevision: 3}
	if err := repo.Start(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := repo.Start(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	conflict := candidate
	conflict.TargetVersion = "3.0.0"
	if !errors.Is(repo.Start(ctx, conflict), reliabilityapp.ErrDeploymentCandidateRequired) {
		t.Fatal("conflicting request accepted")
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := repo.Reconcile(ctx, 1, func(row *reliabilityapp.DeploymentRecord) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			row.State = reliabilityapp.DeploymentPromoted
			return nil
		})
		finished <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	calls := 0
	n, err := repo.Reconcile(ctx, 1, func(*reliabilityapp.DeploymentRecord) error { calls++; return nil })
	close(release)
	if err != nil || n != 0 || calls != 0 {
		t.Fatalf("locked deployment selected: %d %d %v", n, calls, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	// A fresh repository sees the terminal state and cannot repeat it.
	repo, _ = reliabilitypostgres.New(pool)
	n, err = repo.Reconcile(ctx, 1, func(*reliabilityapp.DeploymentRecord) error { t.Error("terminal selected"); return nil })
	if err != nil || n != 0 {
		t.Fatalf("terminal replay: %d %v", n, err)
	}
}
