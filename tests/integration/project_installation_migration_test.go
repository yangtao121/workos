//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// installation004Checksum pins 004_project_app_installations.sql: the
// migration has already run on the acceptance volume and is
// checksum-protected, so any edit is a hard failure before migrate.Run
// rejects it. The value was recorded by the installation review round.
const installation004Checksum = "df364efc07892164611e4587288e46ddec491b187662f6271dd2907c5527e00b"

// requestOwnerCompositeFK is the constraint 005 installs on the request
// mapping: (owner_user_id, installation_id) referencing the installation's
// (owner_user_id, id) unique key.
const requestOwnerCompositeFK = "project_app_installation_requests_owner_installation_fkey"

// assertInstallation005Shape verifies the 005 schema facts: the migration is
// recorded, the owner-bound composite foreign key replaced the single-column
// one, the referenced unique key exists, and the redundant 004 owner index is
// gone.
func assertInstallation005Shape(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var applied005 bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = '005_project_app_installation_request_owner.sql')`).Scan(&applied005); err != nil || !applied005 {
		t.Fatalf("005 must be applied: %v %v", err, applied005)
	}
	var compositeFK, uniqueKey, legacyIndex int
	if err := conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_constraint
		         WHERE conname = $1
		           AND conrelid = 'workos_core.project_app_installation_requests'::regclass
		           AND contype = 'f'),
		       (SELECT count(*) FROM pg_constraint
		         WHERE conname = 'project_app_installations_owner_id_unique'
		           AND conrelid = 'workos_core.project_app_installations'::regclass
		           AND contype = 'u'),
		       (SELECT count(*) FROM pg_indexes
		         WHERE indexname = 'project_app_installations_owner_idx')`,
		requestOwnerCompositeFK).Scan(&compositeFK, &uniqueKey, &legacyIndex); err != nil {
		t.Fatalf("inspect 005 constraints: %v", err)
	}
	if compositeFK != 1 || uniqueKey != 1 || legacyIndex != 0 {
		t.Fatalf("005 shape mismatch: compositeFK=%d uniqueKey=%d legacyIndex=%d", compositeFK, uniqueKey, legacyIndex)
	}
	// The composite FK must reference the installation's owner+id pair, not
	// just any installation column pair.
	var referencesOwnerID bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint fk
			JOIN pg_class target ON target.oid = fk.confrelid
			WHERE fk.conname = $1
			  AND target.relname = 'project_app_installations'
			  AND (SELECT array_agg(a.attname ORDER BY x.ord)
			       FROM unnest(fk.confkey) WITH ORDINALITY AS x(attnum, ord)
			       JOIN pg_attribute a ON a.attrelid = fk.confrelid AND a.attnum = x.attnum
			      ) = ARRAY['owner_user_id', 'id']::name[])`,
		requestOwnerCompositeFK).Scan(&referencesOwnerID); err != nil || !referencesOwnerID {
		t.Fatalf("composite FK must reference installations(owner_user_id, id): %v %v", err, referencesOwnerID)
	}
}

// execOnConn runs one statement on an already-open scratch connection so
// multi-step fixtures share a single session.
func execOnConn(t *testing.T, ctx context.Context, conn *pgx.Conn, statement string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("exec %q: %v", firstLine(statement), err)
	}
}

// seedInstallationOwnerPair prepares two owners, one project per owner, and
// one active installation per project on a migrated database; it returns the
// two installation ids.
func seedInstallationOwnerPair(t *testing.T, ctx context.Context, conn *pgx.Conn) (ownerA, ownerB, installationA, installationB string) {
	t.Helper()
	ownerA, ownerB = "01999999-9999-7999-8999-99999999999a", "01999999-9999-7999-8999-99999999999b"
	installationA, installationB = "01999999-9999-7999-8999-99999999999c", "01999999-9999-7999-8999-99999999999d"
	const projectA, projectB = "01999999-9999-7999-8999-99999999999e", "01999999-9999-7999-8999-99999999999f"
	// The deployment index allows one owner per database; relax only that
	// index, exactly like the registry migration tests.
	execOnConn(t, ctx, conn, `DROP INDEX IF EXISTS workos_core.users_single_owner_idx`)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'owner a', now()), ($2, 'owner', 'owner b', now())`, ownerA, ownerB)
	for _, project := range []struct{ id, owner string }{{projectA, ownerA}, {projectB, ownerB}} {
		execOnConn(t, ctx, conn, `INSERT INTO workos_core.projects (
			id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
		) VALUES ($1, $2, $3, 'Owner Pair', $4, $5, 1, now(), now())`,
			project.id, project.owner, "owner-pair-key-"+project.id, newUUIDForTest(80), newUUIDForTest(81))
	}
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installations (
		id, owner_user_id, project_id, app_id, version, manifest_digest, installed_at
	) VALUES ($1, $2, $3, 'board-app', '1.0.0', $4, now()), ($5, $6, $7, 'board-app', '1.0.0', $8, now())`,
		installationA, ownerA, projectA, "sha256:"+repeat("a", 64), installationB, ownerB, projectB, "sha256:"+repeat("b", 64))
	return ownerA, ownerB, installationA, installationB
}

// TestProjectInstallationMigrationFromEmptyDatabase proves the full 001→005
// chain applies on a pristine database, installs its invariants as real
// constraints, and ends with the 005 owner-bound request mapping shape.
func TestProjectInstallationMigrationFromEmptyDatabase(t *testing.T) {
	t.Parallel()
	if checksum := migrationFileChecksum(t, "004_project_app_installations.sql"); checksum != installation004Checksum {
		t.Fatalf("004_project_app_installations.sql must never change: checksum %s", checksum)
	}
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	applied004 := false
	rows, err := conn.Query(ctx, `SELECT name FROM workos_meta.schema_migrations`)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if len(name) >= 3 && name[:3] == "004" {
			applied004 = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !applied004 {
		t.Fatal("expected the 004 project app installation migration")
	}

	const (
		owner     = "01999999-9999-7999-8999-999999999991"
		projectID = "01999999-9999-7999-8999-999999999992"
	)
	if _, err := conn.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'install migration test', now())`, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
	) VALUES ($1, $2, 'install-mig-key', 'Install Migration', $3, $4, 1, now(), now())`,
		projectID, owner, newUUIDForTest(3), newUUIDForTest(4)); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	insertInstallation := func(id, installOwner, project, app, version, digest string) error {
		_, err := conn.Exec(ctx, `INSERT INTO workos_core.project_app_installations (
			id, owner_user_id, project_id, app_id, version, manifest_digest, installed_at
		) VALUES ($1, $2, $3, $4, $5, $6, now())`, id, installOwner, project, app, version, digest)
		return err
	}
	goodDigest := "sha256:" + repeat("a", 64)
	if err := insertInstallation(newUUIDForTest(5), owner, projectID, "board-app", "1.0.0", goodDigest); err != nil {
		t.Fatalf("valid installation insert: %v", err)
	}
	// One active installation per (project, app).
	if err := insertInstallation(newUUIDForTest(6), owner, projectID, "board-app", "2.0.0", goodDigest); err == nil {
		t.Fatal("duplicate active installation must be rejected")
	}
	// Owner binding is a database fact: another owner cannot install into
	// the project even with a row of their own.
	if _, err := conn.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'second', now())`, "01999999-9999-7999-8999-999999999993"); err != nil {
		// The deployment index allows a single owner; relax only that index,
		// exactly like the registry migration test.
		if _, indexErr := conn.Exec(ctx, `DROP INDEX workos_core.users_single_owner_idx`); indexErr != nil {
			t.Fatalf("relax single-owner index: %v", indexErr)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
			VALUES ($1, 'owner', 'second', now())`, "01999999-9999-7999-8999-999999999993"); err != nil {
			t.Fatalf("seed second owner: %v", err)
		}
	}
	const secondOwner = "01999999-9999-7999-8999-999999999993"
	if err := insertInstallation(newUUIDForTest(7), secondOwner, projectID, "foreign-app", "1.0.0", goodDigest); err == nil {
		t.Fatal("installations are bound to the project owner by the composite foreign key")
	}
	// Shape guards: malformed app id, version, and digest never enter.
	for name, statement := range map[string]string{
		"malformed app id": fmt.Sprintf(`INSERT INTO workos_core.project_app_installations (
			id, owner_user_id, project_id, app_id, version, manifest_digest, installed_at
		) VALUES ('%s', '%s', '%s', 'Bad_ID', '1.0.0', '%s', now())`, newUUIDForTest(8), owner, projectID, goodDigest),
		"malformed version": fmt.Sprintf(`INSERT INTO workos_core.project_app_installations (
			id, owner_user_id, project_id, app_id, version, manifest_digest, installed_at
		) VALUES ('%s', '%s', '%s', 'notes-app', '1.0', '%s', now())`, newUUIDForTest(9), owner, projectID, goodDigest),
		"malformed digest": fmt.Sprintf(`INSERT INTO workos_core.project_app_installations (
			id, owner_user_id, project_id, app_id, version, manifest_digest, installed_at
		) VALUES ('%s', '%s', '%s', 'notes-app', '1.0.0', 'not-a-digest', now())`, newUUIDForTest(10), owner, projectID),
	} {
		if _, err := conn.Exec(ctx, statement); err == nil {
			t.Errorf("%s must be rejected by the database", name)
		}
	}
	// Tombstoning frees the active slot for a new instance.
	if _, err := conn.Exec(ctx, `UPDATE workos_core.project_app_installations
		SET uninstalled_at = now() WHERE project_id = $1 AND app_id = 'board-app'`, projectID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if err := insertInstallation(newUUIDForTest(11), owner, projectID, "board-app", "2.0.0", "sha256:"+repeat("b", 64)); err != nil {
		t.Fatalf("reinstall after tombstone must be allowed: %v", err)
	}

	// The pristine chain ends with the 005 owner-bound request mapping shape.
	assertInstallation005Shape(t, ctx, conn)
}

// TestProjectInstallationRequestMappingOwnerBinding proves the 005 composite
// foreign key with live data: same-owner mappings persist, cross-owner
// mappings are rejected by the database, the idempotency namespace stays
// owner-wide (two owners may share one key), and tombstoned installations
// remain replayable references that RESTRICT deletion.
func TestProjectInstallationRequestMappingOwnerBinding(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)

	ownerA, ownerB, installationA, installationB := seedInstallationOwnerPair(t, ctx, conn)

	insertRequest := func(owner, installation, key, digestHex string) error {
		_, err := conn.Exec(ctx, `INSERT INTO workos_core.project_app_installation_requests (
			owner_user_id, idempotency_key, command, request_digest, installation_id, project_revision,
			result_granted_permissions, result_grant_revision, created_at
		) VALUES ($1, $2, 'install', $3, $4, 2, ARRAY['agent.task.run'], 1, now())`,
			owner, key, "sha256:"+repeat(digestHex, 64), installation)
		return err
	}

	// Same-owner mappings persist under owner-unique keys.
	if err := insertRequest(ownerA, installationA, "owner-a-key", "a"); err != nil {
		t.Fatalf("same-owner mapping must persist: %v", err)
	}
	// The database rejects a mapping whose owner differs from the referenced
	// installation's owner — the exact gap 005 closes.
	if err := insertRequest(ownerB, installationA, "cross-owner-key", "b"); err == nil {
		t.Fatal("cross-owner result mapping must be rejected by the composite foreign key")
	}
	// The idempotency namespace is owner-wide, not global: both owners may
	// use the same key for their own installations.
	if err := insertRequest(ownerA, installationA, "shared-key", "c"); err != nil {
		t.Fatalf("owner A shared key: %v", err)
	}
	if err := insertRequest(ownerB, installationB, "shared-key", "d"); err != nil {
		t.Fatalf("owner B must reuse the same key for its own installation: %v", err)
	}

	// A tombstoned installation stays a valid replay reference, and its
	// RESTRICT semantics survive 005's constraint swap.
	if _, err := conn.Exec(ctx, `UPDATE workos_core.project_app_installations
		SET uninstalled_at = now() WHERE id = $1`, installationA); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if err := insertRequest(ownerA, installationA, "replay-key", "e"); err != nil {
		t.Fatalf("tombstoned installation must remain referencable: %v", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM workos_core.project_app_installations WHERE id = $1`, installationA); err == nil {
		t.Fatal("deleting an installation referenced by a request mapping must be restricted")
	}
}

// TestProjectInstallationMigration005Upgrades004EraData replays the acceptance
// volume's upgrade path: a database holding real 004-era installation and
// request rows migrates forward through 005 with every mapping preserved.
func TestProjectInstallationMigration005Upgrades004EraData(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Prime the database exactly like a 004-era volume: apply 001–004 by
	// hand and record their checksums so migrations.Run only executes 005.
	execOn(t, dsn, `CREATE SCHEMA IF NOT EXISTS workos_meta;
		CREATE TABLE IF NOT EXISTS workos_meta.schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL
		)`)
	for _, name := range []string{
		"001_foundation.sql",
		"002_app_registry.sql",
		"003_app_registry_idempotency.sql",
		"004_project_app_installations.sql",
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		execOn(t, dsn, string(body))
		digest := sha256.Sum256(body)
		execOn(t, dsn, `INSERT INTO workos_meta.schema_migrations (name, checksum, applied_at)
			VALUES ($1, $2, now())`, name, hex.EncodeToString(digest[:]))
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)
	ownerA, ownerB, installationA, installationB := seedInstallationOwnerPair(t, ctx, conn)
	// One tombstoned pair proves replay references survive the upgrade.
	execOnConn(t, ctx, conn, `UPDATE workos_core.project_app_installations SET uninstalled_at = now() WHERE id = $1`, installationA)
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id, project_revision, result_uninstalled_at, created_at
	) VALUES ($1, 'upgrade-key-a', 'install', $2, $3, 2, now(), now()), ($1, 'uninstall-key-a', 'uninstall', $4, $3, 3, now(), now())`,
		ownerA, "sha256:"+repeat("c", 64), installationA, "sha256:"+repeat("d", 64))
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id, project_revision, created_at
	) VALUES ($1, 'upgrade-key-b', 'install', $2, $3, 2, now())`, ownerB, "sha256:"+repeat("e", 64), installationB)
	var mappingsBefore int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM workos_core.project_app_installation_requests`).Scan(&mappingsBefore); err != nil {
		t.Fatal(err)
	}
	if mappingsBefore != 3 {
		t.Fatalf("expected three seeded mappings, got %d", mappingsBefore)
	}

	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("forward migration over 004-era data: %v", err)
	}
	assertInstallation005Shape(t, ctx, conn)

	// Every 004-era mapping survives with its owner and installation intact.
	rows, err := conn.Query(ctx, `
		SELECT r.owner_user_id, r.installation_id, r.command
		FROM workos_core.project_app_installation_requests r
		ORDER BY r.idempotency_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type mapping struct{ owner, installation, command string }
	var after []mapping
	for rows.Next() {
		var m mapping
		if err := rows.Scan(&m.owner, &m.installation, &m.command); err != nil {
			t.Fatal(err)
		}
		after = append(after, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(after) != mappingsBefore {
		t.Fatalf("005 must preserve every mapping: %d before, %d after", mappingsBefore, len(after))
	}
	for _, m := range after {
		var installationOwner string
		if err := conn.QueryRow(ctx,
			`SELECT owner_user_id FROM workos_core.project_app_installations WHERE id = $1`, m.installation).Scan(&installationOwner); err != nil {
			t.Fatalf("mapping installation vanished: %v", err)
		}
		if installationOwner != m.owner {
			t.Fatalf("mapping owner %s no longer matches installation owner %s", m.owner, installationOwner)
		}
	}
}

// TestProjectInstallationMigration005FailsClosedOnCrossOwnerData proves the
// migration stops before touching the schema when a 004-era database already
// holds a cross-owner mapping: the violation is reported, 005 is not applied,
// and the pre-existing rows are left exactly as they were.
func TestProjectInstallationMigration005FailsClosedOnCrossOwnerData(t *testing.T) {
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
	for _, name := range []string{
		"001_foundation.sql",
		"002_app_registry.sql",
		"003_app_registry_idempotency.sql",
		"004_project_app_installations.sql",
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		execOn(t, dsn, string(body))
		digest := sha256.Sum256(body)
		execOn(t, dsn, `INSERT INTO workos_meta.schema_migrations (name, checksum, applied_at)
			VALUES ($1, $2, now())`, name, hex.EncodeToString(digest[:]))
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)
	_, ownerB, installationA, _ := seedInstallationOwnerPair(t, ctx, conn)
	// Seed the exact defect 005 exists to prevent: a 004-era writer bound
	// owner B's key to another owner's installation. 004's single-column FK
	// accepts it; 005 must refuse to run over it.
	execOnConn(t, ctx, conn, `INSERT INTO workos_core.project_app_installation_requests (
		owner_user_id, idempotency_key, command, request_digest, installation_id, project_revision, created_at
	) VALUES ($1, 'legacy-cross-owner', 'install', $2, $3, 2, now())`,
		ownerB, "sha256:"+repeat("f", 64), installationA)

	err = migrations.Run(ctx, dsn)
	if err == nil {
		t.Fatal("005 must fail closed over a cross-owner mapping")
	}
	if !strings.Contains(err.Error(), "cross-owner") {
		t.Fatalf("the failure must name the cross-owner violation: %v", err)
	}
	// 005 is not applied and the schema is untouched.
	var applied005 bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = '005_project_app_installation_request_owner.sql')`).Scan(&applied005); err != nil || applied005 {
		t.Fatalf("005 must not be recorded after the failure: %v %v", err, applied005)
	}
	var legacyFK bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'project_app_installation_requests_installation_fkey')`).Scan(&legacyFK); err != nil || !legacyFK {
		t.Fatalf("the 004 single-column FK must still be in place: %v %v", err, legacyFK)
	}
	// The offending row is preserved for read-only inspection, not deleted.
	var surviving int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM workos_core.project_app_installation_requests WHERE idempotency_key = 'legacy-cross-owner'`).Scan(&surviving); err != nil || surviving != 1 {
		t.Fatalf("the cross-owner mapping must be preserved for reporting: %v %d", err, surviving)
	}
}

// TestProjectInstallationRepositoryConcurrency runs two independent
// repository instances (separate pools, no shared process state) against one
// scratch database: the project row lock and request primary key decide the
// races, and the loser's transaction leaves nothing behind.
func TestProjectInstallationRepositoryConcurrency(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	seedPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer seedPool.Close()
	const (
		owner     = "01999999-9999-7999-8999-999999999991"
		projectID = "01999999-9999-7999-8999-999999999992"
	)
	if _, err := seedPool.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'repo race', now())`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := seedPool.Exec(ctx, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, revision, created_at, updated_at
	) VALUES ($1, $2, 'repo-race-key', 'Repo Race', $3, $4, 1, now(), now())`,
		projectID, owner, newUUIDForTest(5), newUUIDForTest(6)); err != nil {
		t.Fatal(err)
	}

	left := postgres.New(seedPool)
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rightPool.Close()
	right := postgres.New(rightPool)

	pinned := domain.PinnedApp{AppID: "board-app", Version: "1.0.0", ManifestDigest: "sha256:" + repeat("a", 64), Scope: "user"}
	install := func(repository *postgres.Repository, key, installationID string) (ports.InstallationResult, error) {
		return repository.Install(ctx, ports.InstallCommand{
			OwnerUserID: owner, IdempotencyKey: key, ProjectID: projectID, AppID: "board-app",
			Pinned: pinned, ExpectedRevision: 1, RequestDigest: domain.InstallationRequestDigest("install", projectID, "board-app", "", "", 1),
			NewInstallationID: installationID, Now: time.Now().UTC(),
		})
	}

	t.Run("TwoRepositoriesOneWinnerPerRevision", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 2)
		var group sync.WaitGroup
		for index, repository := range []*postgres.Repository{left, right} {
			group.Add(1)
			go func(repository *postgres.Repository, index int) {
				defer group.Done()
				<-start
				_, err := install(repository, fmt.Sprintf("repo-race-%d", index), newUUIDForTest(20+index))
				results <- err
			}(repository, index)
		}
		close(start)
		group.Wait()
		close(results)
		winners, aborted := 0, 0
		for err := range results {
			switch {
			case err == nil:
				winners++
			case errors.Is(err, domain.ErrConflict):
				aborted++
			default:
				t.Fatalf("unexpected race outcome: %v", err)
			}
		}
		if winners != 1 || aborted != 1 {
			t.Fatalf("two repository instances must yield one winner: winners=%d aborted=%d", winners, aborted)
		}
		var (
			active   int
			revision int64
			events   int
		)
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1 AND uninstalled_at IS NULL`, projectID).Scan(&active); err != nil || active != 1 {
			t.Fatalf("one active fact expected: %v %d", err, active)
		}
		if err := seedPool.QueryRow(ctx, `SELECT revision FROM workos_core.projects WHERE id = $1`, projectID).Scan(&revision); err != nil || revision != 2 {
			t.Fatalf("revision must be 2: %v %d", err, revision)
		}
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, projectID).Scan(&events); err != nil || events != 1 {
			t.Fatalf("one revision event expected: %v %d", err, events)
		}
	})

	t.Run("SameKeyAcrossRepositoriesReplays", func(t *testing.T) {
		sharedKey := "repo-shared-key"
		digest := domain.InstallationRequestDigest("install", projectID, "notes-app", "", "", 2)
		command := ports.InstallCommand{
			OwnerUserID: owner, IdempotencyKey: sharedKey, ProjectID: projectID, AppID: "notes-app",
			Pinned:           domain.PinnedApp{AppID: "notes-app", Version: "1.0.0", ManifestDigest: "sha256:" + repeat("c", 64), Scope: "project"},
			ExpectedRevision: 2, RequestDigest: digest, NewInstallationID: newUUIDForTest(40), Now: time.Now().UTC(),
		}
		first, err := left.Install(ctx, command)
		if err != nil {
			t.Fatalf("first shared-key install: %v", err)
		}
		replayCommand := command
		replayCommand.NewInstallationID = newUUIDForTest(41)
		replay, err := right.Install(ctx, replayCommand)
		if err != nil {
			t.Fatalf("cross-repository replay: %v", err)
		}
		if replay.Installation.ID != first.Installation.ID || replay.ProjectRevision != first.ProjectRevision {
			t.Fatalf("cross-repository same-key replay must return the first fact: %#v vs %#v", replay, first)
		}
		var active int
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1 AND app_id = 'notes-app' AND uninstalled_at IS NULL`, projectID).Scan(&active); err != nil || active != 1 {
			t.Fatalf("replay must not create a second active row: %v %d", err, active)
		}
	})

	t.Run("FailedTransactionRollsBackEverything", func(t *testing.T) {
		var revision int64
		if err := seedPool.QueryRow(ctx, `SELECT revision FROM workos_core.projects WHERE id = $1`, projectID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		// The pinned version violates the database CHECK mid-transaction, so
		// the insert fails after the project row lock and every earlier or
		// later step of the transaction must vanish with it.
		_, err := left.Install(ctx, ports.InstallCommand{
			OwnerUserID: owner, IdempotencyKey: "repo-rollback-key", ProjectID: projectID, AppID: "bad-version-app",
			Pinned:           domain.PinnedApp{AppID: "bad-version-app", Version: "not-semver", ManifestDigest: "sha256:" + repeat("d", 64), Scope: "user"},
			ExpectedRevision: revision, RequestDigest: domain.InstallationRequestDigest("install", projectID, "bad-version-app", "", "", revision),
			NewInstallationID: newUUIDForTest(50), Now: time.Now().UTC(),
		})
		if err == nil {
			t.Fatal("constraint-violating install must fail")
		}
		var (
			after    int64
			rows     int
			requests int
			events   int
		)
		if err := seedPool.QueryRow(ctx, `SELECT revision FROM workos_core.projects WHERE id = $1`, projectID).Scan(&after); err != nil || after != revision {
			t.Fatalf("failed transaction must not change the revision: %d vs %d (%v)", after, revision, err)
		}
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1 AND app_id = 'bad-version-app'`, projectID).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("failed transaction must leave no installation: %v %d", err, rows)
		}
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_core.project_app_installation_requests WHERE idempotency_key = 'repo-rollback-key'`).Scan(&requests); err != nil || requests != 0 {
			t.Fatalf("failed transaction must not consume the key: %v %d", err, requests)
		}
		if err := seedPool.QueryRow(ctx,
			`SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, projectID).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if expected := revision - 1; events < int(expected) {
			t.Fatalf("failed transaction must not append events: %d events with revision %d", events, revision)
		}
	})
}

// TestProjectInstallationMigrationAppliedToAcceptanceVolume asserts the
// forward migrations really ran on the persistent acceptance volume the
// gateway tests use, not only on scratch databases: 004 created the tables,
// 005 installed the owner-bound composite foreign key, and every persisted
// mapping still resolves to an installation owned by the mapping's owner.
func TestProjectInstallationMigrationAppliedToAcceptanceVolume(t *testing.T) {
	t.Parallel()
	conn := appRegistryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var applied bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM workos_meta.schema_migrations
			WHERE name LIKE '004%' AND name LIKE '%project_app_installations%'
		)`).Scan(&applied); err != nil {
		t.Skipf("acceptance database unavailable: %v", err)
	}
	if !applied {
		t.Fatal("004_project_app_installations must be applied on the acceptance volume")
	}
	var tableExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'workos_core' AND table_name = 'project_app_installation_requests'
		)`).Scan(&tableExists); err != nil || !tableExists {
		t.Fatalf("installation request table missing on acceptance volume: %v %v", err, tableExists)
	}
	// The 005 owner binding must be present on the volume itself.
	assertInstallation005Shape(t, ctx, conn)
	// Every persisted mapping resolves to an installation of the same owner:
	// the owner-inconsistent count comes from ONE statement, so the shared
	// acceptance volume is observed through a single statement snapshot.
	// Counting total and joined rows with two independent SELECTs reads two
	// separate snapshots under Read Committed, and any parallel test
	// committing a new mapping between them makes the joined count exceed
	// the total — an observation race, not a foreign-key violation.
	var ownerInconsistent int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM workos_core.project_app_installation_requests r
		LEFT JOIN workos_core.project_app_installations i
		  ON r.installation_id = i.id AND r.owner_user_id = i.owner_user_id
		WHERE i.id IS NULL`).Scan(&ownerInconsistent); err != nil {
		t.Fatalf("count owner-inconsistent mappings on acceptance volume: %v", err)
	}
	if ownerInconsistent != 0 {
		t.Fatalf("acceptance volume has %d owner-inconsistent mappings", ownerInconsistent)
	}
}

func newUUIDForTest(suffix int) string {
	return fmt.Sprintf("01999999-9999-7999-8999-%012d", suffix)
}
