//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/migrations"
	reliabilitypostgres "github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	reliabilityapp "github.com/yangtao121/workos/internal/reliability/application"
)

func TestRepairPollingDoesNotStarveBehindUnfinishedTasks(t *testing.T) {
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
	owner, project, installation := id(), id(), id()
	for i := range 9 {
		incident, task := id(), id()
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(incident)))
		_, err := pool.Exec(ctx, `INSERT INTO workos_reliability.incidents
  (id,owner_user_id,project_id,app_instance_id,app_id,workload_id,workload_generation,violation,severity,summary,occurrence_digest,evidence_digest,state,created_at,updated_at)
  VALUES ($1,$2,$3,$4,'repair-fixture',$5,1,'health_failure','warning','Synthetic health failure',$6,$6,'open',$7,$7)`, incident, owner, project, installation, id(), digest, time.Now().UTC().Add(-time.Duration(10-i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.RecordRepairSubmitted(ctx, reliabilityapp.RepairCandidate{IncidentID: incident, OwnerUserID: owner, ProjectID: project, AppInstanceID: installation}, task); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for range 3 {
		// Restarting the repository must retain the poll order.
		repo, err = reliabilitypostgres.New(pool)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := repo.ListRepairCompleted(ctx, 4)
		if err != nil || len(rows) != 4 {
			t.Fatalf("batch=%d err=%v", len(rows), err)
		}
		for _, row := range rows {
			if row.OwnerUserID != owner || row.ProjectID != project || row.AppInstanceID != installation || row.TaskID == "" {
				t.Fatal("poll lost repair scope")
			}
			seen[row.IncidentID] = true
		}
	}
	if len(seen) != 9 {
		t.Fatalf("only %d of 9 submitted tasks polled; unfinished head starved later rows", len(seen))
	}
	for incident := range seen {
		if err := repo.ClearRepairCompleted(ctx, incident); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := repo.ListRepairCompleted(ctx, 4)
	if err != nil || len(rows) != 0 {
		t.Fatalf("terminal repairs still polled: %d %v", len(rows), err)
	}
}
