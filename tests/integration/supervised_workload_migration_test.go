//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/migrations"
	reliabilitypostgres "github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	reliabilitydomain "github.com/yangtao121/workos/internal/reliability/domain"
)

// TestSupervisedWorkloadMigrationsFromEmptyDatabase proves 015/016 apply
// forward-only on a pristine database, apply idempotently, and create
// exactly the module-owned facts with their fail-closed constraints.
func TestSupervisedWorkloadMigrationsFromEmptyDatabase(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	// The second run is a no-op: checksums and applied markers match.
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch pool: %v", err)
	}
	defer pool.Close()

	for _, table := range []struct{ schema, name string }{
		{"workos_runtime", "workloads"},
		{"workos_runtime", "workload_operations"},
		{"workos_reliability", "incidents"},
		{"workos_reliability", "incident_actions"},
		{"workos_reliability", "supervisor_checkpoints"},
		{"workos_reliability", "supervisor_workloads"},
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)",
			table.schema, table.name).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s.%s was not created (err=%v)", table.schema, table.name, err)
		}
	}

	// Seed the owner and project rows the way the acceptance bootstrap does.
	ownerID := "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	deviceID := "0198d7ea-2110-7c42-b659-c5e4d73bc338"
	projectID := "0198d7ea-2110-7c42-b659-c5e4d73bc342"
	instanceID := "0198d7ea-2110-7c42-b659-c5e4d73bc343"
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Supervision Owner', now()) ON CONFLICT DO NOTHING`,
		ownerID); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1, $2, 'supervision-migration-project', 'Supervision Migration', gen_random_uuid(), gen_random_uuid(), now(), now()) ON CONFLICT DO NOTHING`,
		projectID, ownerID); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	workloadID := "0198d7ea-2110-7c42-b659-c5e4d73bc361"
	// A web-bundle session cannot carry a workload reference, and a
	// web-service session cannot carry artifact facts: the renderer
	// coherence CHECK fails closed on both hybrid shapes.
	if _, err := pool.Exec(ctx, `INSERT INTO workos_runtime.surface_sessions (
		id, owner_user_id, device_id, idempotency_key, request_digest, project_id,
		app_instance_id, renderer, app_id, app_version, manifest_digest, artifact_id,
		artifact_digest, entrypoint, path, workload_id, workload_generation,
		installation_grant_revision, created_at, expires_at)
		VALUES ($1, $2, $3, 'hybrid', 'sha256:' || repeat('a', 64), $4, $2, 'web-bundle',
		'note-taker', '1.0.0', 'sha256:' || repeat('a', 64), gen_random_uuid(),
		'sha256:' || repeat('b', 64), 'index.html', $6, $5, 1, 1,
		now(), now() + interval '15 minutes')`,
		"0198d7ea-2110-7c42-b659-c5e4d73bc362", ownerID, deviceID, projectID, workloadID,
		"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc362/"); err == nil {
		t.Fatalf("hybrid web-bundle session with workload reference was accepted")
	} else if !strings.Contains(err.Error(), "surface_sessions_renderer_coherence") && !strings.Contains(err.Error(), "check constraint") {
		t.Fatalf("hybrid session rejected for the wrong reason: %v", err)
	}

	// A running workload without verified engine facts violates the
	// running-facts CHECK.
	if _, err := pool.Exec(ctx, `INSERT INTO workos_runtime.workloads (
		id, owner_user_id, project_id, app_instance_id, app_id, app_version,
		manifest_digest, image, command, port, requested_policy, policy_version,
		effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
		effective_pids_max, effective_startup_seconds, effective_restart_limit,
		generation, state, container_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'container-app', '1.0.0', 'sha256:' || repeat('c', 64),
		'localhost/img@sha256:' || repeat('d', 64), '[":"]', 8080, '{}', 'v1',
		100000, 67108864, 100663296, 32, 10, 2, 1, 'running', $5,
		now(), now())`,
		workloadID, ownerID, projectID, instanceID,
		"workos-wl-0198d7ea-2110-7c42-b659-c5e4d73bc361"); err == nil {
		t.Fatalf("running workload without engine facts was accepted")
	}

	// The same active instance cannot hold two active workloads.
	first := "0198d7ea-2110-7c42-b659-c5e4d73bc363"
	second := "0198d7ea-2110-7c42-b659-c5e4d73bc364"
	insertStartingWorkload := func(id string) error {
		_, err := pool.Exec(ctx, `INSERT INTO workos_runtime.workloads (
			id, owner_user_id, project_id, app_instance_id, app_id, app_version,
			manifest_digest, image, command, port, requested_policy, policy_version,
			effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
			effective_pids_max, effective_startup_seconds, effective_restart_limit,
			generation, state, container_name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'container-app', '1.0.0', 'sha256:' || repeat('c', 64),
			'localhost/img@sha256:' || repeat('d', 64), '["/bin","serve"]', 8080, '{}', 'v1',
			100000, 67108864, 100663296, 32, 10, 2, 1, 'starting', $5,
			now(), now())`,
			id, ownerID, projectID, instanceID,
			"workos-wl-"+id)
		return err
	}
	if err := insertStartingWorkload(first); err != nil {
		t.Fatalf("first active workload: %v", err)
	}
	if err := insertStartingWorkload(second); err == nil {
		t.Fatalf("second active workload for one instance was accepted")
	}
}

var _ = migrations.Run

// TestIncidentAcknowledgeKeyPersistence proves the acknowledge idempotency
// key is a persisted, uniqueness-enforced fact: same key replays the same
// acknowledged state, and a key reused on a different incident of the same
// owner is a stable conflict.
func TestIncidentAcknowledgeKeyPersistence(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	repository, err := reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatalf("repository: %v", err)
	}
	ownerID := "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	now := time.Now().UTC()

	newIncident := func(id string) reliabilitydomain.Incident {
		return reliabilitydomain.Incident{
			ID: id, OwnerUserID: ownerID, ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc342",
			AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc343", AppID: "container-app",
			WorkloadID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", WorkloadGeneration: 1,
			Violation: reliabilitydomain.ViolationUnexpectedExit, Summary: "The app workload exited unexpectedly and was not restarted by the engine.",
			OccurrenceDigest: "sha256:" + strings.Repeat(id[len(id)-1:], 64),
			EvidenceDigest:   "sha256:" + strings.Repeat("e", 64),
			State:            reliabilitydomain.StateOpen, RestartOutcome: reliabilitydomain.OutcomePending,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	first := "0198d7ea-2110-7c42-b659-c5e4d73bc371"
	second := "0198d7ea-2110-7c42-b659-c5e4d73bc372"
	for _, id := range []string{first, second} {
		if created, err := repository.CreateIncident(ctx, newIncident(id)); err != nil || !created {
			t.Fatalf("create %s: created=%v err=%v", id, created, err)
		}
	}
	if err := repository.Acknowledge(ctx, first, ownerID, "ack-key-a", now); err != nil {
		t.Fatalf("acknowledge first: %v", err)
	}
	// Same key, same incident: exact replay.
	if err := repository.Acknowledge(ctx, first, ownerID, "ack-key-a", now); err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	// Same key, different incident of the same owner: stable conflict.
	if err := repository.Acknowledge(ctx, second, ownerID, "ack-key-a", now); !errors.Is(err, reliabilitydomain.ErrIdempotencyConflict) {
		t.Fatalf("key reuse verdict %v, want conflict", err)
	}
	// The second incident acknowledges under its own key.
	if err := repository.Acknowledge(ctx, second, ownerID, "ack-key-b", now); err != nil {
		t.Fatalf("acknowledge second: %v", err)
	}
	stored, err := repository.GetIncident(ctx, first)
	if err != nil || stored.AcknowledgedAt == nil || stored.AcknowledgeKey != "ack-key-a" {
		t.Fatalf("first ack facts stored=%+v err=%v", stored, err)
	}
}
