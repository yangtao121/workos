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
	reliabilityports "github.com/yangtao121/workos/internal/reliability/ports"
	runtimepostgres "github.com/yangtao121/workos/internal/runtime/workload/adapters/postgres"
	runtimedomain "github.com/yangtao121/workos/internal/runtime/workload/domain"
	runtimeports "github.com/yangtao121/workos/internal/runtime/workload/ports"
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
	if _, err := pool.Exec(ctx, `INSERT INTO workos_runtime.workload_operations (
		workload_id, operation_key, operation, request_digest, result_generation,
		error_kind, created_at, updated_at)
		VALUES ($1, 'permanent-failure', 'restart', 'sha256:' || repeat('f', 64), 1,
		'permanent', now(), now())`, first); err != nil {
		t.Fatalf("018 permanent operation verdict was rejected: %v", err)
	}
	runtimeRepository, err := runtimepostgres.New(pool)
	if err != nil {
		t.Fatalf("runtime repository: %v", err)
	}
	transitionedAt := time.Now().UTC()
	openOperation := runtimedomain.WorkloadOperation{
		WorkloadID: first, OperationKey: "open-request", Operation: runtimedomain.OperationTerminate,
		RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultGeneration: 1,
		CreatedAt: transitionedAt, UpdatedAt: transitionedAt,
	}
	if err := runtimeRepository.RecordOperation(ctx, openOperation); err != nil {
		t.Fatalf("record open operation: %v", err)
	}
	conflictingOperation := openOperation
	conflictingOperation.RequestDigest = "sha256:" + strings.Repeat("b", 64)
	conflictingOperation.ErrorKind = runtimedomain.ErrorUnavailable
	if err := runtimeRepository.RecordOperation(ctx, conflictingOperation); !errors.Is(err, runtimedomain.ErrIdempotencyConflict) {
		t.Fatalf("open operation rebind verdict %v, want conflict", err)
	}
	var openDigest string
	var openErrorKind *string
	if err := pool.QueryRow(ctx, `SELECT request_digest, error_kind
		FROM workos_runtime.workload_operations
		WHERE workload_id = $1 AND operation_key = 'open-request'`, first).Scan(&openDigest, &openErrorKind); err != nil {
		t.Fatalf("read open operation: %v", err)
	}
	if openDigest != openOperation.RequestDigest || openErrorKind != nil {
		t.Fatalf("open operation was rebound: digest=%s error_kind=%v", openDigest, openErrorKind)
	}
	if err := runtimeRepository.TransitionOperation(ctx, first, runtimedomain.StateStarting, runtimedomain.StateFailed, runtimeports.WorkloadFacts{
		Generation: 1, RestartCount: 0, HealthVerdict: runtimedomain.HealthFailing,
		LastExit: runtimedomain.ExitUnknown, StoppedAt: &transitionedAt, ClearEngine: true,
	}, runtimedomain.WorkloadOperation{
		WorkloadID: first, OperationKey: "permanent-failure", Operation: runtimedomain.OperationRestart,
		RequestDigest: "sha256:" + strings.Repeat("f", 64), ResultGeneration: 1,
		ErrorKind: runtimedomain.ErrorUnavailable, CreatedAt: transitionedAt, UpdatedAt: transitionedAt,
	}, transitionedAt); !errors.Is(err, runtimedomain.ErrIdempotencyConflict) {
		t.Fatalf("terminal operation downgrade verdict %v, want conflict", err)
	}
	var state, errorKind string
	if err := pool.QueryRow(ctx, `SELECT w.state, o.error_kind
		FROM workos_runtime.workloads w
		JOIN workos_runtime.workload_operations o ON o.workload_id = w.id
		WHERE w.id = $1 AND o.operation_key = 'permanent-failure'`, first).Scan(&state, &errorKind); err != nil {
		t.Fatalf("read atomic transition verdict: %v", err)
	}
	if state != "starting" || errorKind != "permanent" {
		t.Fatalf("state/operation transaction partially committed: state=%s error_kind=%s", state, errorKind)
	}
	// Every lifecycle and idle write is generation-guarded. A reconcile pass
	// holding generation 1 facts must not be able to fail or alter the idle
	// clock of a generation 2 workload after a concurrent restart.
	startedAt := transitionedAt.Add(time.Second)
	runningFacts := runtimeports.WorkloadFacts{
		ContainerID: "container-generation-1", Endpoint: "127.0.0.1:18080",
		CgroupPath: "/user.slice/workos-generation-1", Generation: 1,
		HealthVerdict: runtimedomain.HealthOK, LastExit: runtimedomain.ExitNone,
		StartedAt: &startedAt,
	}
	if err := runtimeRepository.Transition(ctx, first, runtimedomain.StateStarting, runtimedomain.StateRunning, runningFacts, startedAt); err != nil {
		t.Fatalf("seed generation 1 running: %v", err)
	}
	if err := runtimeRepository.Transition(ctx, first, runtimedomain.StateRunning, runtimedomain.StateStarting, runtimeports.WorkloadFacts{
		Generation: 2, RestartCount: 1, HealthVerdict: runtimedomain.HealthUnknown,
		LastExit: runtimedomain.ExitNone, ClearEngine: true,
	}, startedAt.Add(time.Second)); err != nil {
		t.Fatalf("seed generation 2 starting: %v", err)
	}
	startedAt = startedAt.Add(2 * time.Second)
	runningFacts.ContainerID = "container-generation-2"
	runningFacts.Endpoint = "127.0.0.1:18081"
	runningFacts.CgroupPath = "/user.slice/workos-generation-2"
	runningFacts.Generation = 2
	runningFacts.RestartCount = 1
	runningFacts.StartedAt = &startedAt
	if err := runtimeRepository.Transition(ctx, first, runtimedomain.StateStarting, runtimedomain.StateRunning, runningFacts, startedAt); err != nil {
		t.Fatalf("seed generation 2 running: %v", err)
	}
	staleStoppedAt := startedAt.Add(time.Second)
	if err := runtimeRepository.Transition(ctx, first, runtimedomain.StateRunning, runtimedomain.StateFailed, runtimeports.WorkloadFacts{
		Generation: 1, RestartCount: 0, HealthVerdict: runtimedomain.HealthFailing,
		LastExit: runtimedomain.ExitUnknown, StoppedAt: &staleStoppedAt, ClearEngine: true,
	}, staleStoppedAt); !errors.Is(err, runtimedomain.ErrNotFound) {
		t.Fatalf("stale lifecycle transition verdict %v, want guarded not found", err)
	}
	if _, err := runtimeRepository.SetIdle(ctx, first, 1, true, staleStoppedAt); !errors.Is(err, runtimedomain.ErrNotFound) {
		t.Fatalf("stale idle mark verdict %v, want guarded not found", err)
	}
	idleSince, err := runtimeRepository.SetIdle(ctx, first, 2, true, staleStoppedAt)
	if err != nil || idleSince == nil {
		t.Fatalf("mark generation 2 idle: idle_since=%v err=%v", idleSince, err)
	}
	if _, err := runtimeRepository.SetIdle(ctx, first, 1, false, staleStoppedAt); !errors.Is(err, runtimedomain.ErrNotFound) {
		t.Fatalf("stale idle clear verdict %v, want guarded not found", err)
	}
	var generation int64
	var idleStamp *time.Time
	if err := pool.QueryRow(ctx, `SELECT state, generation, idle_since FROM workos_runtime.workloads WHERE id = $1`, first).Scan(&state, &generation, &idleStamp); err != nil {
		t.Fatalf("read generation-guarded workload: %v", err)
	}
	if state != "running" || generation != 2 || idleStamp == nil {
		t.Fatalf("stale write changed newer workload: state=%s generation=%d idle_since=%v", state, generation, idleStamp)
	}
	if err := insertStartingWorkload(second); err == nil {
		t.Fatalf("second active workload for one instance was accepted")
	}
}

// TestIncidentConvergenceConstraints proves that resolution is a two-step
// mitigated→resolved transition, pending-action recovery is a real private
// query (not a wildcard owner query), and the unsupported control outcome is
// accepted by the reliability-owned action ledger.
func TestIncidentConvergenceConstraints(t *testing.T) {
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
	now := time.Now().UTC()
	incident := reliabilitydomain.Incident{
		ID:            "0198d7ea-2110-7c42-b659-c5e4d73bc381",
		OwnerUserID:   "0198d7ea-2110-7c42-b659-c5e4d73bc341",
		ProjectID:     "0198d7ea-2110-7c42-b659-c5e4d73bc342",
		AppInstanceID: "0198d7ea-2110-7c42-b659-c5e4d73bc343",
		AppID:         "container-app", WorkloadID: "0198d7ea-2110-7c42-b659-c5e4d73bc361",
		WorkloadGeneration: 1, Violation: reliabilitydomain.ViolationUnexpectedExit,
		Summary:          reliabilitydomain.ViolationUnexpectedExit.Summary(),
		OccurrenceDigest: "sha256:" + strings.Repeat("8", 64),
		EvidenceDigest:   "sha256:" + strings.Repeat("9", 64),
		State:            reliabilitydomain.StateOpen, RestartOutcome: reliabilitydomain.OutcomePending,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if created, err := repository.CreateIncident(ctx, incident); err != nil || !created {
		t.Fatalf("create incident: created=%v err=%v", created, err)
	}
	if err := repository.MarkResolved(ctx, incident.ID, now.Add(time.Second)); !errors.Is(err, reliabilitydomain.ErrNotFound) {
		t.Fatalf("open→resolved verdict %v, want guarded not found", err)
	}
	stored, err := repository.GetIncident(ctx, incident.ID)
	if err != nil || stored.State != reliabilitydomain.StateOpen || stored.MitigatedAt != nil || stored.ResolvedAt != nil {
		t.Fatalf("open incident changed during refused resolution: %+v err=%v", stored, err)
	}
	pending, err := repository.ListPendingActionIncidents(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != incident.ID {
		t.Fatalf("pending action incidents=%+v err=%v", pending, err)
	}
	// A retryable unavailable action must rotate behind never-attempted work.
	// Otherwise a fixed-size recovery batch can select the same old rows on
	// every poll and permanently starve a newly observed workload.
	retried := incident
	retried.ID = "0198d7ea-2110-7c42-b659-c5e4d73bc383"
	retried.OccurrenceDigest = "sha256:" + strings.Repeat("4", 64)
	retried.CreatedAt = now.Add(-2 * time.Minute)
	retried.UpdatedAt = retried.CreatedAt
	if created, createErr := repository.CreateIncident(ctx, retried); createErr != nil || !created {
		t.Fatalf("create retried incident: created=%v err=%v", created, createErr)
	}
	fresh := incident
	fresh.ID = "0198d7ea-2110-7c42-b659-c5e4d73bc384"
	fresh.OccurrenceDigest = "sha256:" + strings.Repeat("5", 64)
	fresh.CreatedAt = now.Add(-time.Minute)
	fresh.UpdatedAt = fresh.CreatedAt
	if created, createErr := repository.CreateIncident(ctx, fresh); createErr != nil || !created {
		t.Fatalf("create fresh incident: created=%v err=%v", created, createErr)
	}
	if actionErr := repository.RecordAction(ctx, retried.ID, "restart", reliabilityports.ControlResult{
		Outcome: reliabilityports.ControlUnavailable,
	}, now); actionErr != nil {
		t.Fatalf("record retryable unavailable action: %v", actionErr)
	}
	next, listErr := repository.ListPendingActionIncidents(ctx, 1)
	if listErr != nil || len(next) != 1 || next[0].ID != fresh.ID {
		t.Fatalf("pending fairness next=%+v err=%v, want fresh incident", next, listErr)
	}
	if err := repository.RecordAction(ctx, incident.ID, "restart", reliabilityports.ControlResult{
		Outcome: reliabilityports.ControlUnsupported,
	}, now); err != nil {
		t.Fatalf("019 unsupported action outcome was rejected: %v", err)
	}
	action, err := repository.LookupAction(ctx, incident.ID, "restart")
	if err != nil || action.Outcome != reliabilityports.ControlUnsupported {
		t.Fatalf("stored action=%+v err=%v", action, err)
	}
	if err := repository.RecordAction(ctx, incident.ID, "restart", reliabilityports.ControlResult{
		Outcome: reliabilityports.ControlUnavailable,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("late unavailable action write: %v", err)
	}
	action, err = repository.LookupAction(ctx, incident.ID, "restart")
	if err != nil || action.Outcome != reliabilityports.ControlUnsupported {
		t.Fatalf("terminal action verdict was downgraded: action=%+v err=%v", action, err)
	}
	if err := repository.UpdateOutcome(ctx, incident.ID, reliabilitydomain.StateMitigated, reliabilitydomain.OutcomeRestarted, now.Add(time.Second)); err != nil {
		t.Fatalf("mitigate: %v", err)
	}
	if err := repository.MarkResolved(ctx, incident.ID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("resolve mitigated incident: %v", err)
	}
	stored, err = repository.GetIncident(ctx, incident.ID)
	if err != nil || stored.State != reliabilitydomain.StateResolved || stored.MitigatedAt == nil || stored.ResolvedAt == nil {
		t.Fatalf("resolved incident=%+v err=%v", stored, err)
	}
	failed := incident
	failed.ID = "0198d7ea-2110-7c42-b659-c5e4d73bc382"
	failed.OccurrenceDigest = "sha256:" + strings.Repeat("6", 64)
	failed.EvidenceDigest = "sha256:" + strings.Repeat("7", 64)
	if created, err := repository.CreateIncident(ctx, failed); err != nil || !created {
		t.Fatalf("create failed-outcome incident: created=%v err=%v", created, err)
	}
	if err := repository.UpdateOutcome(ctx, failed.ID, reliabilitydomain.StateOpen, reliabilitydomain.OutcomeFailed, now); err != nil {
		t.Fatalf("record terminal failed outcome: %v", err)
	}
	if err := repository.UpdateOutcome(ctx, failed.ID, reliabilitydomain.StateMitigated, reliabilitydomain.OutcomeRestarted, now.Add(time.Second)); !errors.Is(err, reliabilitydomain.ErrNotFound) {
		t.Fatalf("terminal incident outcome overwrite verdict %v, want guarded not found", err)
	}
	failedStored, err := repository.GetIncident(ctx, failed.ID)
	if err != nil || failedStored.State != reliabilitydomain.StateOpen || failedStored.RestartOutcome != reliabilitydomain.OutcomeFailed {
		t.Fatalf("terminal failed outcome changed: incident=%+v err=%v", failedStored, err)
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
