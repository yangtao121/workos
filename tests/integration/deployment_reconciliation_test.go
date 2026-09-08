//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func TestDeploymentIncludesStartupIncidentsWithinScope(t *testing.T) {
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
	// Freeze the two boundaries: a fault during startup precedes canary health
	// success, and must remain visible after the full observation window starts.
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `UPDATE workos_reliability.deployment_ledger SET created_at=$2, canary_started_at=$3, canary_until=$4, state='canary' WHERE incident_id=$1`, candidate.IncidentID, start, start.Add(30*time.Second), start.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	insert := func(incident, owner, project, installation string, at time.Time) {
		t.Helper()
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(incident)))
		_, err := pool.Exec(ctx, `INSERT INTO workos_reliability.incidents
  (id,owner_user_id,project_id,app_instance_id,app_id,workload_id,workload_generation,violation,severity,summary,occurrence_digest,evidence_digest,state,created_at,updated_at)
  VALUES ($1,$2,$3,$4,'deployment-fixture',$5,1,'health_failure','warning','Synthetic health failure',$6,$6,'open',$7,$7)`, incident, owner, project, installation, id(), digest, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want bool) {
		t.Helper()
		_, err := repo.Reconcile(ctx, 1, func(row *reliabilityapp.DeploymentRecord) error {
			if row.NewIncident != want {
				t.Errorf("new incident=%v want=%v", row.NewIncident, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(candidate.IncidentID, candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID, start.Add(time.Second))
	insert(id(), candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID, start.Add(-time.Second))
	insert(id(), id(), candidate.ProjectID, candidate.InstallationID, start.Add(time.Second))
	insert(id(), candidate.OwnerUserID, id(), candidate.InstallationID, start.Add(time.Second))
	insert(id(), candidate.OwnerUserID, candidate.ProjectID, id(), start.Add(time.Second))
	check(false)
	insert(id(), candidate.OwnerUserID, candidate.ProjectID, candidate.InstallationID, start.Add(time.Second))
	check(true)
	repo, err = reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	check(true)
}
