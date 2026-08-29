//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// pinnedMigrationChecksums fixes migrations 001–012 byte for byte. Every one
// of them has already run on the acceptance volume; any edit is a hard
// failure before migrations.Run even rejects it. 013 is the only file this
// task is allowed to add.
var pinnedMigrationChecksums = map[string]string{
	"001_foundation.sql":                             "f748516e52ae915e0582a8dfa5de665a6590264268d0e55c46659746bcaa0378",
	"002_app_registry.sql":                           "f3a353fb0ffdf51cafc44e6fda63dba5fc55f436c2830c53bd0e972ed2504947",
	"003_app_registry_idempotency.sql":               "73766b95799bce3e0f4569e49940df044fd287ae723f38ed7f410c719e83ebe3",
	"004_project_app_installations.sql":              "df364efc07892164611e4587288e46ddec491b187662f6271dd2907c5527e00b",
	"005_project_app_installation_request_owner.sql": "45cb2bb4abb590656cb119e0517af3a220f94d279c43bec1eec754c5bf0a8781",
	"006_web_bundle_artifacts.sql":                   "628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27",
	"007_surface_sessions.sql":                       "b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986",
	"008_project_installation_grants.sql":            "180ba05df3c54c45d16dd1c67f8b45cacdde8d6ac1a77ae5338abc3dd0055766",
	"009_agent_app_task_provenance.sql":              "233ea77ca9f3dc0d18362c0cc2a650eb288c5bc90d0c0e01e3ec9428b6f411db",
	"010_surface_bridge_tokens.sql":                  "91f47007a071915e0d6c2b39f35f2611f2b1f30c72781d113fd801368045896a",
	"011_mutable_project_app_grants.sql":             "1b85383b53f23829151cacca44c5f400f1fb9ca1e06f4836767a3c40f354775f",
	"012_surface_grant_revision.sql":                 "9b8335b1a7936ef96b5b5aaeeeac8b351768bb5c98152bfed6d80bbd904bcc89",
}

// orderedPinnedMigrations lists 001–012 in application order.
func orderedPinnedMigrations() []string {
	names := make([]string, 0, len(pinnedMigrationChecksums))
	for name := range pinnedMigrationChecksums {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func readMigrationFile(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return body
}

// TestProjectCreateRequestsMigrationChain pins the full migration surface of
// this task: every pre-existing file is byte-identical, the pristine chain
// applies through 013, the 013 authority shape is real, and a second run is
// a no-op.
func TestProjectCreateRequestsMigrationChain(t *testing.T) {
	t.Parallel()
	for name, checksum := range pinnedMigrationChecksums {
		if actual := migrationFileChecksum(t, name); actual != checksum {
			t.Fatalf("%s must never change: checksum %s", name, actual)
		}
	}
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migration run must be a no-op: %v", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)
	assertProjectCreateRequestsShape(t, ctx, conn)
}

// assertProjectCreateRequestsShape verifies the 013 authority is installed
// with the owner-scoped primary key, the digest grammar, and the versioned
// result marker.
func assertProjectCreateRequestsShape(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var applied bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = '013_project_create_requests.sql')`).Scan(&applied); err != nil || !applied {
		t.Fatalf("013 must be applied: %v %v", err, applied)
	}
	var table int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'workos_core' AND table_name = 'project_create_requests'`).Scan(&table); err != nil || table != 1 {
		t.Fatalf("project_create_requests table missing: %v %d", err, table)
	}
	// Reuse the database's existing owner when there is one — the shape
	// assertions must never mutate the volume's single-owner fact — and
	// seed one only on an empty database.
	shapeOwner := "01999999-9999-7999-8999-999999999981"
	var existingOwner string
	err := conn.QueryRow(ctx, `SELECT id FROM workos_core.users WHERE kind = 'owner' ORDER BY id LIMIT 1`).Scan(&existingOwner)
	switch {
	case err == nil:
		shapeOwner = existingOwner
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := conn.Exec(ctx, `
			INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'shape', now())`,
			shapeOwner); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
	default:
		t.Fatalf("find owner for shape assertions: %v", err)
	}
	// The successful fixture key is unique per run: the acceptance volume
	// persists between runs, and a stale fixture row must never fail a
	// later run's primary-key arbitration probe.
	shapeKey := fmt.Sprintf("shape-key-%d", time.Now().UnixNano())
	insert := func(key, digest, result string) error {
		_, err := conn.Exec(ctx, `
			INSERT INTO workos_core.project_create_requests (
				owner_user_id, idempotency_key, request_digest, result, created_at
			) VALUES ($1, $2, $3, $4, now())`,
			shapeOwner, key, digest, result)
		return err
	}
	validResult := `{"result_version":"1","project":{}}`
	if err := insert(shapeKey, "sha256:"+repeat("a", 64), validResult); err != nil {
		t.Fatalf("legal mapping insert must persist: %v", err)
	}
	if err := insert(shapeKey, "sha256:"+repeat("a", 64), validResult); err == nil {
		t.Fatal("the (owner, key) primary key must arbitrate same-key races")
	}
	if err := insert("shape-bad-digest", "not-a-digest", validResult); err == nil {
		t.Fatal("malformed digest must be rejected")
	}
	if err := insert("shape-bad-result", "sha256:"+repeat("b", 64), `{"result_version":"2","project":{}}`); err == nil {
		t.Fatal("unknown result version must be rejected")
	}
}

// TestProjectCreateRequestsMigrationForwardsLegacyVolume replays the
// acceptance upgrade path: a database holding real 001–012 project rows
// (none of which have a create-request mapping) migrates forward through 013
// without fabricating mappings for them.
func TestProjectCreateRequestsMigrationForwardsLegacyVolume(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	execOn(t, dsn, `CREATE SCHEMA IF NOT EXISTS workos_meta;
		CREATE TABLE IF NOT EXISTS workos_meta.schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL
		)`)
	for _, name := range orderedPinnedMigrations() {
		execOn(t, dsn, string(readMigrationFile(t, name)))
		execOn(t, dsn, `INSERT INTO workos_meta.schema_migrations (name, checksum, applied_at)
			VALUES ($1, $2, now())`, name, pinnedMigrationChecksums[name])
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)
	owner, project := "01999999-9999-7999-8999-999999999982", "01999999-9999-7999-8999-999999999983"
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'legacy volume', now())`, owner)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
	) VALUES ($1, $2, 'legacy-key', 'Legacy Project', $3, $4, 1, now(), now())`,
		project, owner, "01999999-9999-7999-8999-999999999984", "01999999-9999-7999-8999-999999999985")

	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("forward migration over legacy volume: %v", err)
	}
	assertProjectCreateRequestsShape(t, ctx, conn)
	// The legacy row survives untouched and no mapping is invented for it.
	var legacyRows, mappings int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_core.projects WHERE id = $1 AND idempotency_key = 'legacy-key'`, project).Scan(&legacyRows); err != nil || legacyRows != 1 {
		t.Fatalf("legacy project must survive 013: %v %d", err, legacyRows)
	}
	// The shape helper above legitimately inserts its own fixture mapping;
	// the assertion is that nothing was invented for the legacy row's key.
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'legacy-key'`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("013 must not fabricate mappings for legacy rows: %v %d", err, mappings)
	}
}

// TestProjectCreateRequestsAppliedToAcceptanceVolume asserts 013 really ran
// on the persistent acceptance volume the stack uses.
func TestProjectCreateRequestsAppliedToAcceptanceVolume(t *testing.T) {
	t.Parallel()
	conn := appRegistryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var applied bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = '013_project_create_requests.sql')`).Scan(&applied); err != nil {
		t.Skipf("acceptance database unavailable: %v", err)
	}
	if !applied {
		t.Fatal("013_project_create_requests must be applied on the acceptance volume")
	}
	assertProjectCreateRequestsShape(t, ctx, conn)
}

// contractHarness prepares one scratch database with the full migration
// chain and two independent repository instances on separate pools, so
// concurrency tests never share in-process state.
type contractHarness struct {
	owner string
	dsn   string
	left  *postgres.Repository
	right *postgres.Repository
	poolA *pgxpool.Pool
	poolB *pgxpool.Pool
}

func newContractHarness(t *testing.T) *contractHarness {
	t.Helper()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := migrations.Run(ctx, dsn); err != nil {
		cancel()
		t.Fatalf("migrations: %v", err)
	}
	cancel()
	poolA, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolB, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { poolA.Close(); poolB.Close() })
	harness := &contractHarness{
		owner: "01999999-9999-7999-8999-999999999990",
		dsn:   dsn,
		left:  postgres.New(poolA), right: postgres.New(poolB),
		poolA: poolA, poolB: poolB,
	}
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSeed()
	if _, err := poolA.Exec(seedCtx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'contract harness', now())`, harness.owner); err != nil {
		t.Fatal(err)
	}
	return harness
}

// createCommand builds one fully validated create command for the harness
// owner with the given name.
func (h *contractHarness) createCommand(t *testing.T, key, name, projectID string, now time.Time) ports.CreateCommand {
	t.Helper()
	project := domain.Project{
		ID: projectID, OwnerUserID: h.owner, Name: name,
		WorkspaceRefs:         []domain.WorkspaceRef{{ID: "ws-1", Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/x"}},
		InstalledAppIDs:       []string{},
		KnowledgeCollectionID: "01999999-9999-7999-8999-999999999995",
		ArtifactCollectionID:  "01999999-9999-7999-8999-999999999996",
		Revision:              1, CreatedAt: now, UpdatedAt: now,
	}
	digest := domain.CreateRequestDigest(project.Name, project.Icon, project.WorkspaceRefs, project.HarnessBinding)
	return ports.CreateCommand{Project: project, IdempotencyKey: key, RequestDigest: digest, Now: now}
}

// count runs one counting assertion against the harness database.
func (h *contractHarness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var total int
	if err := h.poolA.QueryRow(context.Background(), query, args...).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

// mustCountZero asserts a rollback left nothing behind.
func (h *contractHarness) mustCountZero(t *testing.T, name, query string) {
	t.Helper()
	if total := h.count(t, query); total != 0 {
		t.Errorf("%s must leave nothing behind: %d", name, total)
	}
}

// TestProjectCreateIdempotencyAgainstRealPostgres is the durable-idempotency
// heart of this task, executed against real PostgreSQL with two independent
// pools: exact first-response replay, conflict, legacy fail-closed, failure
// rollback, transient outage, commit failure, restart replay, and real
// concurrency.
func TestProjectCreateIdempotencyAgainstRealPostgres(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	now := time.Date(2026, 8, 29, 8, 30, 0, 0, time.UTC)
	key := "contract-create-key"

	t.Run("SameRequestReplaysExactFirstResponse", func(t *testing.T) {
		command := harness.createCommand(t, key, "Idempotent Project", "01999999-9999-7999-8999-999999999991", now)
		first, err := harness.left.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("first create: %v", err)
		}
		// A different pool — as a second process would be — replays the
		// identical request.
		replayed, err := harness.right.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replayed.ID != first.ID || replayed.Revision != first.Revision ||
			!replayed.CreatedAt.Equal(first.CreatedAt) || !replayed.UpdatedAt.Equal(first.UpdatedAt) ||
			replayed.Name != first.Name || len(replayed.WorkspaceRefs) != len(first.WorkspaceRefs) {
			t.Fatalf("replay must return the exact first response:\nfirst   %#v\nreplay  %#v", first, replayed)
		}
		if harness.count(t, `SELECT count(*) FROM workos_core.projects WHERE owner_user_id = $1`, harness.owner) != 1 {
			t.Fatal("replay must not create a second project")
		}
		if harness.count(t, `SELECT count(*) FROM workos_events.events WHERE stream_id = $1`, first.ID) != 1 {
			t.Fatal("replay must not append a second event")
		}
		if harness.count(t, `SELECT count(*) FROM workos_events.outbox WHERE aggregate_id = $1`, first.ID) != 1 {
			t.Fatal("replay must not append a second outbox row")
		}
	})

	t.Run("ReplaySurvivesUpdateAndArchive", func(t *testing.T) {
		projectID := "01999999-9999-7999-8999-999999999992"
		command := harness.createCommand(t, "mutated-key", "Mutable Project", projectID, now)
		first, err := harness.left.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		updated := first
		updated.Name = "Renamed Later"
		updated.Revision = 2
		updated.UpdatedAt = now.Add(time.Hour)
		if _, err := harness.left.UpdateProject(context.Background(), updated, 1); err != nil {
			t.Fatalf("update: %v", err)
		}
		replayed, err := harness.right.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("replay after update: %v", err)
		}
		if replayed.Name != "Mutable Project" || replayed.Revision != 1 || !replayed.UpdatedAt.Equal(first.UpdatedAt) {
			t.Fatalf("replay must return the first response, not the mutated row: %#v", replayed)
		}
		if _, err := harness.left.ArchiveProject(context.Background(), harness.owner, projectID, 2); err != nil {
			t.Fatalf("archive: %v", err)
		}
		replayedAfterArchive, err := harness.right.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("replay after archive: %v", err)
		}
		if replayedAfterArchive.ArchivedAt != nil || replayedAfterArchive.Revision != 1 {
			t.Fatalf("archive must never leak into the create replay: %#v", replayedAfterArchive)
		}
	})

	t.Run("DifferentRequestOnSameKeyConflicts", func(t *testing.T) {
		command := harness.createCommand(t, "conflict-key", "Original", "01999999-9999-7999-8999-999999999993", now)
		if _, err := harness.left.CreateProject(context.Background(), command); err != nil {
			t.Fatalf("first create: %v", err)
		}
		renamed := command
		renamed.Project.Name = "Different Name"
		renamed.Project.ID = "01999999-9999-7999-8999-999999999994"
		renamed.RequestDigest = domain.CreateRequestDigest(renamed.Project.Name, renamed.Project.Icon, renamed.Project.WorkspaceRefs, renamed.Project.HarnessBinding)
		_, err := harness.right.CreateProject(context.Background(), renamed)
		if !errors.Is(err, domain.ErrIdempotencyConflict) {
			t.Fatalf("different request must conflict, got %v", err)
		}
		harness.mustCountZero(t, "conflicting create project",
			`SELECT count(*) FROM workos_core.projects WHERE id = '01999999-9999-7999-8999-999999999994'`)
	})

	t.Run("LegacyKeyFailsClosed", func(t *testing.T) {
		// A pre-013 project row: the key exists on the mutable row, but no
		// request digest or first-response snapshot was ever recorded. The
		// only honest verdict for any create replay is fail closed.
		if _, err := harness.poolA.Exec(context.Background(), `INSERT INTO workos_core.projects (
			id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
		) VALUES ($1, $2, 'legacy-contract-key', 'Legacy Row', $3, $4, 1, now(), now())`,
			"01999999-9999-7999-8999-999999999995", harness.owner,
			"01999999-9999-7999-8999-999999999996", "01999999-9999-7999-8999-999999999997"); err != nil {
			t.Fatal(err)
		}
		command := harness.createCommand(t, "legacy-contract-key", "Whatever Request", "01999999-9999-7999-8999-999999999998", now)
		_, err := harness.left.CreateProject(context.Background(), command)
		if !errors.Is(err, domain.ErrIdempotencyConflict) {
			t.Fatalf("legacy key must fail closed, got %v", err)
		}
		harness.mustCountZero(t, "fabricated mapping",
			`SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'legacy-contract-key'`)
		harness.mustCountZero(t, "legacy fail-closed project",
			`SELECT count(*) FROM workos_core.projects WHERE id = '01999999-9999-7999-8999-999999999998'`)
	})

	t.Run("ConcurrentSameRequestOneWinnerAndReplay", func(t *testing.T) {
		command := harness.createCommand(t, "race-same-key", "Raced Same", "01999999-9999-7999-8999-999999999999", now)
		start := make(chan struct{})
		type outcome struct {
			project domain.Project
			err     error
		}
		results := make(chan outcome, 2)
		var group sync.WaitGroup
		for _, repository := range []*postgres.Repository{harness.left, harness.right} {
			group.Add(1)
			go func(repository *postgres.Repository) {
				defer group.Done()
				<-start
				project, err := repository.CreateProject(context.Background(), command)
				results <- outcome{project: project, err: err}
			}(repository)
		}
		close(start)
		group.Wait()
		close(results)
		projectIDs := make([]string, 0, 2)
		for result := range results {
			// The database guarantees one winner and one replay; a loser
			// error would mean the adjudication is not durable.
			if result.err != nil {
				t.Fatalf("same-request race must not produce a loser error, got %v", result.err)
			}
			projectIDs = append(projectIDs, result.project.ID)
		}
		if len(projectIDs) != 2 || projectIDs[0] != projectIDs[1] {
			t.Fatalf("replay must return the winner's project: %v", projectIDs)
		}
		// 991, 992, 993, the legacy 995, and this race's single winner.
		if total := harness.count(t, `SELECT count(*) FROM workos_core.projects WHERE owner_user_id = $1`, harness.owner); total != 5 {
			t.Fatalf("race must yield exactly one additional project: %d", total)
		}
		if total := harness.count(t, `SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'race-same-key'`); total != 1 {
			t.Fatalf("race must consume the key exactly once: %d", total)
		}
		if total := harness.count(t, `SELECT count(*) FROM workos_events.events WHERE stream_id = '01999999-9999-7999-8999-999999999999'`); total != 1 {
			t.Fatalf("race must append exactly one event: %d", total)
		}
		if total := harness.count(t, `SELECT count(*) FROM workos_events.outbox WHERE aggregate_id = '01999999-9999-7999-8999-999999999999'`); total != 1 {
			t.Fatalf("race must append exactly one outbox row: %d", total)
		}
	})

	t.Run("ConcurrentDifferentRequestOneWinnerOneConflict", func(t *testing.T) {
		first := harness.createCommand(t, "race-diff-key", "Diff A", "01999999-9999-7999-8999-99999999999a", now)
		second := first
		second.Project.Name = "Diff B"
		second.Project.ID = "01999999-9999-7999-8999-99999999999b"
		second.RequestDigest = domain.CreateRequestDigest(second.Project.Name, second.Project.Icon, second.Project.WorkspaceRefs, second.Project.HarnessBinding)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var group sync.WaitGroup
		for index, repository := range []*postgres.Repository{harness.left, harness.right} {
			command := first
			if index == 1 {
				command = second
			}
			group.Add(1)
			go func(repository *postgres.Repository, command ports.CreateCommand) {
				defer group.Done()
				<-start
				_, err := repository.CreateProject(context.Background(), command)
				errs <- err
			}(repository, command)
		}
		close(start)
		group.Wait()
		close(errs)
		winners, conflicts := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				winners++
			case errors.Is(err, domain.ErrIdempotencyConflict):
				conflicts++
			default:
				t.Fatalf("unexpected race outcome: %v", err)
			}
		}
		if winners != 1 || conflicts != 1 {
			t.Fatalf("exactly one winner and one conflict expected: %d/%d", winners, conflicts)
		}
		if total := harness.count(t, `SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'race-diff-key'`); total != 1 {
			t.Fatalf("the key must be consumed once: %d", total)
		}
	})

	t.Run("EventInsertFailureRollsBackEverything", func(t *testing.T) {
		projectID := "01999999-9999-7999-8999-99999999998a"
		// Pre-seed the event the create transaction will try to append, so
		// the UNIQUE (stream_type, stream_id, sequence) constraint fails the
		// transaction after the project insert succeeded.
		if _, err := harness.poolA.Exec(context.Background(), `INSERT INTO workos_events.events (
			id, stream_type, stream_id, sequence, event_type, payload, occurred_at
		) VALUES ('01999999-9999-7999-8999-99999999998b', 'project', $1, 1, 'project.created.v1', '{}', now())`, projectID); err != nil {
			t.Fatal(err)
		}
		command := harness.createCommand(t, "rollback-key", "Rollback Probe", projectID, now)
		_, err := harness.left.CreateProject(context.Background(), command)
		if err == nil {
			t.Fatal("event insert failure must fail the create")
		}
		if errors.Is(err, ports.ErrStoreUnavailable) {
			t.Fatalf("constraint failure must not be classified as an outage: %v", err)
		}
		harness.mustCountZero(t, "project",
			`SELECT count(*) FROM workos_core.projects WHERE id = '`+projectID+`'`)
		harness.mustCountZero(t, "mapping",
			`SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'rollback-key'`)
		harness.mustCountZero(t, "outbox",
			`SELECT count(*) FROM workos_events.outbox WHERE aggregate_id = '`+projectID+`'`)
		if _, found, lookupErr := harness.left.LookupCreateRequest(context.Background(), harness.owner, "rollback-key"); lookupErr != nil || found {
			t.Fatalf("failed create must not consume the key: found=%v err=%v", found, lookupErr)
		}
	})

	t.Run("TransientEventFailureIsUnavailable", func(t *testing.T) {
		projectID := "01999999-9999-7999-8999-99999999998c"
		// A trigger raising a connection-class SQLSTATE makes the event
		// insert fail with a real mid-transaction transient error — the
		// exact shape a database shutdown produces — classified at the port
		// boundary as an outage.
		if _, err := harness.poolA.Exec(context.Background(), `
			CREATE FUNCTION workos_core.raise_transient() RETURNS trigger AS $$
			BEGIN
				RAISE EXCEPTION 'injected connection failure' USING ERRCODE = '08006';
			END $$ LANGUAGE plpgsql`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			harness.poolA.Exec(context.Background(), `DROP FUNCTION IF EXISTS workos_core.raise_transient()`) //nolint:errcheck
		})
		if _, err := harness.poolA.Exec(context.Background(), `
			CREATE TRIGGER fail_project_event_transient
			BEFORE INSERT ON workos_events.events
			FOR EACH ROW EXECUTE FUNCTION workos_core.raise_transient()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			harness.poolA.Exec(context.Background(), `DROP TRIGGER IF EXISTS fail_project_event_transient ON workos_events.events`) //nolint:errcheck
		})
		command := harness.createCommand(t, "transient-key", "Transient Probe", projectID, now)
		_, err := harness.left.CreateProject(context.Background(), command)
		if !errors.Is(err, ports.ErrStoreUnavailable) {
			t.Fatalf("mid-transaction transient failure must carry the sentinel, got %v", err)
		}
		harness.mustCountZero(t, "transient project",
			`SELECT count(*) FROM workos_core.projects WHERE id = '`+projectID+`'`)
		if _, found, _ := harness.left.LookupCreateRequest(context.Background(), harness.owner, "transient-key"); found {
			t.Fatal("transient failure must not consume the key")
		}
	})

	t.Run("CommitFailureRollsBackEverything", func(t *testing.T) {
		projectID := "01999999-9999-7999-8999-99999999998d"
		if _, err := harness.poolA.Exec(context.Background(), `
			CREATE FUNCTION workos_core.raise_commit_failure() RETURNS trigger AS $$
			BEGIN
				RAISE EXCEPTION 'injected commit failure' USING ERRCODE = '23505';
			END $$ LANGUAGE plpgsql`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			harness.poolA.Exec(context.Background(), `DROP FUNCTION IF EXISTS workos_core.raise_commit_failure()`) //nolint:errcheck
		})
		// A deferred constraint trigger fires exactly at COMMIT, so the
		// failure surfaces on the commit path after every statement
		// succeeded.
		if _, err := harness.poolA.Exec(context.Background(), `
			CREATE CONSTRAINT TRIGGER fail_project_create_commit
			AFTER INSERT ON workos_core.projects
			DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW EXECUTE FUNCTION workos_core.raise_commit_failure()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			harness.poolA.Exec(context.Background(), `DROP TRIGGER IF EXISTS fail_project_create_commit ON workos_core.projects`) //nolint:errcheck
		})
		command := harness.createCommand(t, "commit-fail-key", "Commit Probe", projectID, now)
		_, err := harness.left.CreateProject(context.Background(), command)
		if err == nil {
			t.Fatal("commit failure must fail the create")
		}
		if errors.Is(err, ports.ErrStoreUnavailable) {
			t.Fatalf("commit failure is an internal verdict, not an outage: %v", err)
		}
		harness.mustCountZero(t, "commit-failed project",
			`SELECT count(*) FROM workos_core.projects WHERE id = '`+projectID+`'`)
		harness.mustCountZero(t, "commit-failed mapping",
			`SELECT count(*) FROM workos_core.project_create_requests WHERE idempotency_key = 'commit-fail-key'`)
		harness.mustCountZero(t, "commit-failed event",
			`SELECT count(*) FROM workos_events.events WHERE stream_id = '`+projectID+`'`)
		harness.mustCountZero(t, "commit-failed outbox",
			`SELECT count(*) FROM workos_events.outbox WHERE aggregate_id = '`+projectID+`'`)
	})

	t.Run("RestartReplaysFirstResponseSnapshot", func(t *testing.T) {
		command := harness.createCommand(t, "restart-key", "Restarted Project", "01999999-9999-7999-8999-99999999998e", now)
		first, err := harness.left.CreateProject(context.Background(), command)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		// A truly fresh pool over a fresh connection set — the process
		// restart equivalent: no in-process state can answer the replay.
		freshPool, err := pgxpool.New(context.Background(), harness.dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer freshPool.Close()
		restarted := postgres.New(freshPool)
		stored, found, err := restarted.LookupCreateRequest(context.Background(), harness.owner, "restart-key")
		if err != nil || !found {
			t.Fatalf("consumed key must persist: found=%v err=%v", found, err)
		}
		if stored.RequestDigest != command.RequestDigest {
			t.Fatalf("persisted digest must match: %s vs %s", stored.RequestDigest, command.RequestDigest)
		}
		if stored.Result.ID != first.ID || stored.Result.Revision != 1 || !stored.Result.CreatedAt.Equal(first.CreatedAt) {
			t.Fatalf("persisted snapshot must equal the first response: %#v vs %#v", stored.Result, first)
		}
	})
}

// TestProjectListPaginationWalksExactly seeds a controlled fixture, walks
// every page through the application's page result, and verifies tokens are
// exact, pages neither repeat nor lose items, and the archived filter stays
// consistent. The fixture records exactly the IDs it created; the scratch
// database lifecycle removes everything.
func TestProjectListPaginationWalksExactly(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	service := application.New(harness.left, ids.UUIDv7{})
	now := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	const total = 7
	const pageSize = 3
	created := make([]string, 0, total)
	for index := 0; index < total; index++ {
		projectID := fmt.Sprintf("01999999-9999-7999-8999-9999999999%02x", index+0xa0)
		created = append(created, projectID)
		command := harness.createCommand(t, fmt.Sprintf("paging-key-%02d", index), fmt.Sprintf("Paging %02d", index), projectID, now.Add(time.Duration(index)*time.Second))
		if _, err := harness.left.CreateProject(context.Background(), command); err != nil {
			t.Fatalf("seed project %d: %v", index, err)
		}
	}
	// Archive one project in the middle so the filter is exercised on a
	// later page, not just the first.
	if _, err := harness.left.ArchiveProject(context.Background(), harness.owner, created[4], 1); err != nil {
		t.Fatalf("archive fixture: %v", err)
	}

	walk := func(includeArchived bool) []string {
		t.Helper()
		var seen []string
		cursor := ""
		for page := 0; ; page++ {
			result, err := service.ListProjects(context.Background(), harness.owner, cursor, pageSize, includeArchived)
			if err != nil {
				t.Fatalf("page %d: %v", page, err)
			}
			if len(result.Items) > pageSize {
				t.Fatalf("page %d exceeded the page size: %d", page, len(result.Items))
			}
			for _, project := range result.Items {
				seen = append(seen, project.ID)
			}
			// An exactly-full final page is the one case where a full
			// page legitimately carries no token; the loop bound and the
			// sequence assertions below prove termination and honesty.
			if result.NextToken == "" {
				break
			}
			cursor = result.NextToken
			if page > 10 {
				t.Fatal("pagination must terminate with the seeded fixture size")
			}
		}
		return seen
	}

	activeSeen := walk(false)
	expectedActive := make([]string, 0, total-1)
	for index, id := range created {
		if index != 4 {
			expectedActive = append(expectedActive, id)
		}
	}
	if len(activeSeen) != len(expectedActive) {
		t.Fatalf("active walk must see exactly %d projects, got %d", len(expectedActive), len(activeSeen))
	}
	for index, id := range expectedActive {
		if activeSeen[index] != id {
			t.Fatalf("walk mismatch at %d: got %s want %s", index, activeSeen[index], id)
		}
	}
	allSeen := walk(true)
	if len(allSeen) != total {
		t.Fatalf("archived-inclusive walk must see all %d projects, got %d", total, len(allSeen))
	}
	for _, walkResult := range [][]string{activeSeen, allSeen} {
		duplicates := map[string]bool{}
		for _, id := range walkResult {
			if duplicates[id] {
				t.Fatalf("project %s repeated within one walk", id)
			}
			duplicates[id] = true
		}
	}
	for _, id := range activeSeen {
		if id == created[4] {
			t.Fatalf("archived project %s must not appear in the active walk", id)
		}
	}
	// A second owner sees nothing of the fixture.
	foreign, err := service.ListProjects(context.Background(), "01999999-9999-7999-8999-99999999998f", "", pageSize, true)
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign owner must see no fixture projects: %v %d", err, len(foreign.Items))
	}
}

// TestProjectServiceCreatePathEndToEndOnRealPostgres drives the full public
// create path (validation → digest → transaction → replay) on real
// PostgreSQL through the application service with the real id generator.
func TestProjectServiceCreatePathEndToEndOnRealPostgres(t *testing.T) {
	t.Parallel()
	harness := newContractHarness(t)
	service := application.New(harness.left, ids.UUIDv7{})
	ctx := context.Background()
	input := application.CreateInput{
		OwnerUserID: harness.owner, IdempotencyKey: "e2e-contract-key", Name: "  E2E Contract  ", Icon: "◈",
		WorkspaceRefs: []domain.WorkspaceRef{{ID: "ws", Kind: "WORKSPACE_KIND_DATASET", URI: "file:///data"}},
	}
	first, err := service.Create(ctx, input)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if first.Revision != 1 || first.Name != "E2E Contract" {
		t.Fatalf("unexpected first response: %#v", first)
	}
	replay, err := service.Create(ctx, input)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ID != first.ID || !replay.CreatedAt.Equal(first.CreatedAt) || replay.Revision != 1 {
		t.Fatalf("replay must be equivalent through the full stack: %#v vs %#v", replay, first)
	}
	conflict := input
	conflict.Name = "Different"
	if _, err := service.Create(ctx, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different name on same key must conflict, got %v", err)
	}
	if _, err := service.Create(ctx, application.CreateInput{OwnerUserID: harness.owner, IdempotencyKey: "bad\nkey", Name: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("control-character key must be invalid, got %v", err)
	}
}
