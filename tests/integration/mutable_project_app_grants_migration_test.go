//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yangtao121/workos/internal/platform/migrations"
)

// This file pins the mutable-grants storage layer (ADR-0003): migration 011
// (owner: workos-core Project Installation) adds the installation grant epoch,
// the 'set-grants' command, and the precise first-response result snapshot;
// migration 012 (owner: runtime-host Surface) adds the session-persisted grant
// epoch snapshot. 001..010 stay byte-identical (TestAllMigrationChecksumsArePinned).

// migrationsThrough010 lists the immutable history a pre-011 volume carries.
var migrationsThrough010 = []string{
	"001_foundation.sql",
	"002_app_registry.sql",
	"003_app_registry_idempotency.sql",
	"004_project_app_installations.sql",
	"005_project_app_installation_request_owner.sql",
	"006_web_bundle_artifacts.sql",
	"007_surface_sessions.sql",
	"008_project_installation_grants.sql",
	"009_agent_app_task_provenance.sql",
	"010_surface_bridge_tokens.sql",
}

// primeMigrationsThrough applies the named migration files by hand and records
// their real checksums, exactly like a volume that stopped at that point in
// history, so the subsequent migrations.Run executes only what remains.
func primeMigrationsThrough(t *testing.T, dsn string, names []string) {
	t.Helper()
	execOn(t, dsn, `CREATE SCHEMA IF NOT EXISTS workos_meta;
		CREATE TABLE IF NOT EXISTS workos_meta.schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL
		)`)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		execOn(t, dsn, string(body))
		digest := sha256.Sum256(body)
		execOn(t, dsn, `INSERT INTO workos_meta.schema_migrations (name, checksum, applied_at)
			VALUES ($1, $2, now())`, name, hex.EncodeToString(digest[:]))
	}
}

// execConnRejected asserts the statement fails on an open connection so shape
// guards can prove constraints bite.
func execConnRejected(t *testing.T, ctx context.Context, conn *pgx.Conn, label, statement string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, statement, args...); err == nil {
		t.Fatalf("%s must be rejected by the database", label)
	}
}

// assertMigrationApplied fails unless the named migration is recorded.
func assertMigrationApplied(t *testing.T, ctx context.Context, conn *pgx.Conn, name string, want bool) {
	t.Helper()
	var applied bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
		t.Fatalf("inspect migration %s: %v", name, err)
	}
	if applied != want {
		t.Fatalf("migration %s applied=%v, want %v", name, applied, want)
	}
}

// seedMutableGrantsOwner prepares one owner and one project on a database that
// already carries the foundation tables.
func seedMutableGrantsOwner(t *testing.T, ctx context.Context, conn *pgx.Conn, owner, project string) {
	t.Helper()
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'mutable grants migration', now())`, owner)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
	) VALUES ($1, $2, 'mutable-grants-mig-key', 'Mutable Grants Migration', $3, $4, 1, now(), now())`,
		project, owner, newUUIDForTest(204), newUUIDForTest(205))
}

// insertMutableGrantsInstallation inserts one installation with an explicit
// grant set; tombstone also records an uninstall timestamp.
func insertMutableGrantsInstallation(t *testing.T, ctx context.Context, conn *pgx.Conn, id, owner, project string, grants []string, tombstone bool) {
	t.Helper()
	uninstalledAt := "NULL"
	if tombstone {
		uninstalledAt = "now()"
	}
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installations (
		id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, installed_at, uninstalled_at
	) VALUES ($1, $2, $3, $4, '1.0.0', $5, $6, now(), `+uninstalledAt+`)`,
		id, owner, project, "app-"+strings.TrimPrefix(id, "01999999-9999-7999-8999-"),
		"sha256:"+repeat("a", 64), grants)
}

// insertMutableGrantsSession inserts one valid web-bundle surface session; the
// grant-revision column is deliberately not listed so the insert works both
// before 012 (column absent) and after it (column default applies).
func insertMutableGrantsSession(t *testing.T, ctx context.Context, conn *pgx.Conn, id, owner string) {
	t.Helper()
	execOnConn(t, ctx, conn, `INSERT INTO workos_runtime.surface_sessions (
		id, owner_user_id, device_id, idempotency_key, request_digest,
		project_id, app_instance_id, renderer, app_id, app_version,
		manifest_digest, artifact_id, artifact_digest, entrypoint, path,
		created_at, expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, 'web-bundle', 'notes-app', '1.0.0',
		$8, $9, $10, 'index.html', $11,
		now(), now() + interval '15 minutes')`,
		id, owner, newUUIDForTest(206), "mutable-grants-session-"+id,
		"sha256:"+repeat("a", 64), newUUIDForTest(207), newUUIDForTest(208),
		"sha256:"+repeat("b", 64), newUUIDForTest(209), "sha256:"+repeat("c", 64),
		"/surfaces/"+id+"/")
}

// TestMutableProjectAppGrantsMigrationsFromPristineDatabase proves 011/012
// apply forward-only on a pristine database (a second run is a no-op), install
// their constraints as real schema facts, keep the two process owners free of
// cross-schema foreign keys, and leave the request namespace ready for the
// 'set-grants' command.
func TestMutableProjectAppGrantsMigrationsFromPristineDatabase(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migrations run: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	assertMigrationApplied(t, ctx, conn, "011_mutable_project_app_grants.sql", true)
	assertMigrationApplied(t, ctx, conn, "012_surface_grant_revision.sql", true)

	// Column facts: grant epochs and result snapshots are NOT NULL with the
	// documented defaults.
	type columnFact struct {
		schema, table, column, nullable, fallback string
	}
	for _, fact := range []columnFact{
		{"workos_core", "project_app_installations", "grant_revision", "NO", "1"},
		{"workos_core", "project_app_installation_requests", "result_granted_permissions", "NO", "'{}'::text[]"},
		{"workos_runtime", "surface_sessions", "installation_grant_revision", "NO", "1"},
	} {
		var nullable, fallback string
		if err := conn.QueryRow(ctx, `
			SELECT is_nullable, coalesce(column_default, '') FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
			fact.schema, fact.table, fact.column).Scan(&nullable, &fallback); err != nil {
			t.Fatalf("column %s.%s missing: %v", fact.table, fact.column, err)
		}
		if nullable != fact.nullable || fallback != fact.fallback {
			t.Fatalf("column %s.%s nullable=%s default=%s, want %s/%s", fact.table, fact.column, nullable, fallback, fact.nullable, fact.fallback)
		}
	}
	// result_grant_revision is NOT NULL with no default: every future writer
	// must state the epoch explicitly instead of inheriting a silent one.
	var resultRevisionDefault string
	if err := conn.QueryRow(ctx, `
		SELECT coalesce(column_default, '') FROM information_schema.columns
		WHERE table_schema = 'workos_core' AND table_name = 'project_app_installation_requests'
		  AND column_name = 'result_grant_revision'`).Scan(&resultRevisionDefault); err != nil || resultRevisionDefault != "" {
		t.Fatalf("result_grant_revision must be NOT NULL without a default: %v %q", err, resultRevisionDefault)
	}

	// Named positive-epoch CHECK constraints exist on both owners' tables.
	for _, constraint := range []struct{ schema, table, name string }{
		{"workos_core", "project_app_installations", "project_app_installations_grant_revision_positive"},
		{"workos_runtime", "surface_sessions", "surface_sessions_installation_grant_revision_positive"},
	} {
		var found int
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_constraint
			WHERE conname = $1 AND conrelid = ($2 || '.' || $3)::regclass AND contype = 'c'`,
			constraint.name, constraint.schema, constraint.table).Scan(&found); err != nil || found != 1 {
			t.Fatalf("check constraint %s missing: %v %d", constraint.name, err, found)
		}
	}

	// The request namespace accepts the new command and still rejects unknown
	// ones; both epochs enforce positivity with live rows.
	const (
		owner        = "01999999-9999-7999-8999-999999999901"
		project      = "01999999-9999-7999-8999-999999999902"
		installation = "01999999-9999-7999-8999-999999999903"
		session      = "01999999-9999-7999-8999-999999999904"
	)
	seedMutableGrantsOwner(t, ctx, conn, owner, project)
	insertMutableGrantsInstallation(t, ctx, conn, installation, owner, project, []string{"agent.event.watch", "agent.task.run"}, false)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id,
		project_revision, result_granted_permissions, result_grant_revision, created_at
	) VALUES ($1, 'set-grants-accepted', 'set-grants', $2, $3, 2, ARRAY['agent.task.run'], 2, now())`,
		owner, "sha256:"+repeat("a", 64), installation)
	execConnRejected(t, ctx, conn, "unknown command",
		`INSERT INTO workos_core.project_app_installation_requests (
			owner_user_id, idempotency_key, command, request_digest, installation_id,
			project_revision, result_granted_permissions, result_grant_revision, created_at
		) VALUES ($1, 'unknown-command-rejected', 're-grant', $2, $3, 2, '{}', 1, now())`,
		owner, "sha256:"+repeat("b", 64), installation)
	execConnRejected(t, ctx, conn, "zero installation grant revision",
		`UPDATE workos_core.project_app_installations SET grant_revision = 0 WHERE id = $1`, installation)

	insertMutableGrantsSession(t, ctx, conn, session, owner)
	var sessionRevision int64
	if err := conn.QueryRow(ctx, `
		SELECT installation_grant_revision FROM workos_runtime.surface_sessions WHERE id = $1`, session).Scan(&sessionRevision); err != nil || sessionRevision != 1 {
		t.Fatalf("fresh session revision = %d (err %v), want the default 1", sessionRevision, err)
	}
	execConnRejected(t, ctx, conn, "zero session grant revision",
		`UPDATE workos_runtime.surface_sessions SET installation_grant_revision = 0 WHERE id = $1`, session)

	// No foreign key crosses the Core/runtime schema boundary in either
	// direction: revocation is decided by revision comparison on private RPCs,
	// never by shared schema.
	var crossSchemaFKs int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.referential_constraints
		WHERE (constraint_schema = 'workos_core' AND unique_constraint_schema = 'workos_runtime')
		   OR (constraint_schema = 'workos_runtime' AND unique_constraint_schema = 'workos_core')`).Scan(&crossSchemaFKs); err != nil {
		t.Fatalf("inspect cross-schema foreign keys: %v", err)
	}
	if crossSchemaFKs != 0 {
		t.Fatalf("found %d cross-schema foreign keys, want 0", crossSchemaFKs)
	}
}

// TestMutableProjectAppGrantsMigration011BackfillsResultSnapshots replays the
// acceptance volume's upgrade path: a 010-era database holding real
// installations with grants and consumed install/uninstall keys migrates
// forward through 011/012 with every mapping's first-response grant and epoch
// snapshotted from its owner-bound installation, and existing surface sessions
// backfill to epoch 1.
func TestMutableProjectAppGrantsMigration011BackfillsResultSnapshots(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	primeMigrationsThrough(t, dsn, migrationsThrough010)

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	const (
		owner             = "01999999-9999-7999-8999-999999999911"
		project           = "01999999-9999-7999-8999-999999999912"
		activeInstall     = "01999999-9999-7999-8999-999999999913"
		tombstonedInstall = "01999999-9999-7999-8999-999999999914"
		preExistingSess   = "01999999-9999-7999-8999-999999999915"
	)
	seedMutableGrantsOwner(t, ctx, conn, owner, project)
	insertMutableGrantsInstallation(t, ctx, conn, activeInstall, owner, project,
		[]string{"agent.event.watch", "agent.task.run"}, false)
	insertMutableGrantsInstallation(t, ctx, conn, tombstonedInstall, owner, project,
		[]string{"agent.task.run"}, true)
	insertMutableGrantsSession(t, ctx, conn, preExistingSess, owner)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id,
		project_revision, result_uninstalled_at, created_at
	) VALUES ($1, 'upgrade-install-key', 'install', $2, $3, 2, NULL, now()),
	         ($1, 'upgrade-uninstall-key', 'uninstall', $4, $5, 3, now(), now())`,
		owner, "sha256:"+repeat("c", 64), activeInstall, "sha256:"+repeat("d", 64), tombstonedInstall)

	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("forward migration over 010-era data: %v", err)
	}
	assertMigrationApplied(t, ctx, conn, "011_mutable_project_app_grants.sql", true)
	assertMigrationApplied(t, ctx, conn, "012_surface_grant_revision.sql", true)

	// Every installation backfills to epoch 1: no mutation path existed.
	var activeEpoch, tombstonedEpoch int64
	if err := conn.QueryRow(ctx, `SELECT grant_revision FROM workos_core.project_app_installations WHERE id = $1`, activeInstall).Scan(&activeEpoch); err != nil || activeEpoch != 1 {
		t.Fatalf("active installation epoch = %d (err %v), want 1", activeEpoch, err)
	}
	if err := conn.QueryRow(ctx, `SELECT grant_revision FROM workos_core.project_app_installations WHERE id = $1`, tombstonedInstall).Scan(&tombstonedEpoch); err != nil || tombstonedEpoch != 1 {
		t.Fatalf("tombstoned installation epoch = %d (err %v), want 1", tombstonedEpoch, err)
	}

	// Each mapping's result snapshot equals its owner-bound installation's
	// grant at migration time plus epoch 1 — the precise first-response fact,
	// not a default and not derivable from a later mutated row.
	rows, err := conn.Query(ctx, `
		SELECT r.idempotency_key, r.command, r.result_granted_permissions, r.result_grant_revision
		FROM workos_core.project_app_installation_requests r
		WHERE r.idempotency_key IN ('upgrade-install-key', 'upgrade-uninstall-key')
		ORDER BY r.idempotency_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	snapshots := map[string][2]any{}
	for rows.Next() {
		var key, command string
		var grants []string
		var revision int64
		if err := rows.Scan(&key, &command, &grants, &revision); err != nil {
			t.Fatal(err)
		}
		snapshots[key] = [2]any{grants, revision}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected two upgraded mappings, got %d", len(snapshots))
	}
	installSnapshot := snapshots["upgrade-install-key"]
	if grants, ok := installSnapshot[0].([]string); !ok || strings.Join(grants, ",") != "agent.event.watch,agent.task.run" {
		t.Fatalf("install snapshot grants = %v, want the active installation's grant", installSnapshot[0])
	}
	if revision, ok := installSnapshot[1].(int64); !ok || revision != 1 {
		t.Fatalf("install snapshot revision = %v, want 1", installSnapshot[1])
	}
	uninstallSnapshot := snapshots["upgrade-uninstall-key"]
	if grants, ok := uninstallSnapshot[0].([]string); !ok || strings.Join(grants, ",") != "agent.task.run" {
		t.Fatalf("uninstall snapshot grants = %v, want the tombstoned installation's grant", uninstallSnapshot[0])
	}
	if revision, ok := uninstallSnapshot[1].(int64); !ok || revision != 1 {
		t.Fatalf("uninstall snapshot revision = %v, want 1", uninstallSnapshot[1])
	}

	// The pre-012 surface session backfills to epoch 1.
	var sessionRevision int64
	if err := conn.QueryRow(ctx, `
		SELECT installation_grant_revision FROM workos_runtime.surface_sessions WHERE id = $1`, preExistingSess).Scan(&sessionRevision); err != nil || sessionRevision != 1 {
		t.Fatalf("pre-existing session revision = %d (err %v), want backfill 1", sessionRevision, err)
	}

	// A second run is a no-op.
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migrations run: %v", err)
	}
}

// TestMutableProjectAppGrantsMigration011FailsClosedOnOrphanMapping proves
// the fail-closed guard: a 010-era request mapping whose owner-bound
// installation no longer resolves (orphaned or owner-inconsistent data) stops
// the migration before any schema change, with the offending row preserved
// for reporting instead of being silently dropped or rewritten. The 005
// composite foreign key makes this state unreachable for real writers, so the
// fixture simulates storage corruption by removing that constraint.
func TestMutableProjectAppGrantsMigration011FailsClosedOnOrphanMapping(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	primeMigrationsThrough(t, dsn, migrationsThrough010)

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	const (
		owner        = "01999999-9999-7999-8999-999999999921"
		project      = "01999999-9999-7999-8999-999999999922"
		installation = "01999999-9999-7999-8999-999999999923"
	)
	seedMutableGrantsOwner(t, ctx, conn, owner, project)
	insertMutableGrantsInstallation(t, ctx, conn, installation, owner, project,
		[]string{"agent.task.run"}, false)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id,
		project_revision, created_at
	) VALUES ($1, 'orphan-mapping', 'install', $2, $3, 2, now())`,
		owner, "sha256:"+repeat("e", 64), installation)
	// Simulate corruption the 005 constraint would otherwise prevent: detach
	// the mapping from its installation so no owner-bound row can resolve it.
	execOnConn(t, ctx, conn, `ALTER TABLE workos_core.project_app_installation_requests
		DROP CONSTRAINT project_app_installation_requests_owner_installation_fkey`)
	execOnConn(t, ctx, conn, `DELETE FROM workos_core.project_app_installations WHERE id = $1`, installation)

	err = migrations.Run(ctx, dsn)
	if err == nil {
		t.Fatal("011 must fail closed over an unbackfillable result mapping")
	}
	if !strings.Contains(err.Error(), "owner-bound installation") {
		t.Fatalf("the failure must name the unbackfillable mapping: %v", err)
	}
	// Neither new migration is recorded and the schema is untouched.
	assertMigrationApplied(t, ctx, conn, "011_mutable_project_app_grants.sql", false)
	assertMigrationApplied(t, ctx, conn, "012_surface_grant_revision.sql", false)
	var grantColumn int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'workos_core' AND table_name = 'project_app_installations'
		  AND column_name = 'grant_revision'`).Scan(&grantColumn); err != nil || grantColumn != 0 {
		t.Fatalf("failed migration must leave the schema untouched: %v %d", err, grantColumn)
	}
	// The offending row is preserved for read-only inspection, not deleted.
	var surviving int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM workos_core.project_app_installation_requests
		WHERE idempotency_key = 'orphan-mapping'`).Scan(&surviving); err != nil || surviving != 1 {
		t.Fatalf("the orphan mapping must be preserved for reporting: %v %d", err, surviving)
	}
}
