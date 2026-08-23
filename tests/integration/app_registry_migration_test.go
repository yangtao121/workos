//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// appRegistry002Checksum pins 002_app_registry.sql: the migration has already
// run on the acceptance volume and is checksum-protected, so any edit to the
// file is a hard failure even before migrate.Run rejects it.
const appRegistry002Checksum = "f3a353fb0ffdf51cafc44e6fda63dba5fc55f436c2830c53bd0e972ed2504947"

func migrationFileChecksum(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// scratchDatabase creates an isolated database and returns its DSN plus a
// cleanup that drops it.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	scratch := fmt.Sprintf("workos_migration_test_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, withDatabase(databaseURL, "postgres"))
	if err != nil {
		t.Skipf("postgres is not reachable for the migration check: %v", err)
	}
	defer admin.Close(context.Background()) //nolint:errcheck
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, scratch)); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, scratch)) //nolint:errcheck
	})
	return withDatabase(databaseURL, scratch)
}

func execOn(t *testing.T, dsn string, statement string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	if _, err := conn.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("exec %q: %v", firstLine(statement), err)
	}
}

func execFails(t *testing.T, dsn string, statement string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	if _, err := conn.Exec(ctx, statement, args...); err == nil {
		t.Fatalf("statement must be rejected by the database: %q", firstLine(statement))
	}
}

func firstLine(statement string) string {
	for _, line := range statement {
		if line != '\n' {
			return fmt.Sprintf("%c…", line)
		}
	}
	return statement
}

// TestAppRegistryMigrationsFromEmptyDatabase proves the migration chain
// (001, 002, 003) applies on a pristine PostgreSQL 18 database and leaves the
// authoritative idempotency mapping in place.
func TestAppRegistryMigrationsFromEmptyDatabase(t *testing.T) {
	t.Parallel()
	if checksum := migrationFileChecksum(t, "002_app_registry.sql"); checksum != appRegistry002Checksum {
		t.Fatalf("002_app_registry.sql must never change: checksum %s", checksum)
	}

	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck

	rows, err := conn.Query(ctx, `SELECT name FROM workos_meta.schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan migration name: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrations: %v", err)
	}
	if !found["001_foundation.sql"] || !found["002_app_registry.sql"] {
		t.Fatalf("expected foundation and app registry migrations on the empty database, got %v", found)
	}
	applied003 := false
	for name := range found {
		if len(name) >= 3 && name[:3] == "003" {
			applied003 = true
		}
	}
	if !applied003 {
		t.Fatalf("expected the 003 idempotency migration on the empty database, got %v", found)
	}

	var mappingExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'workos_core' AND table_name = 'app_registration_requests'
		)`).Scan(&mappingExists); err != nil || !mappingExists {
		t.Fatalf("app_registration_requests table missing after migration: %v %v", err, mappingExists)
	}
	// The legacy idempotency columns must be gone: the mapping table is the
	// single idempotency authority, never a second ruling fact source.
	var legacyColumns int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'workos_core' AND table_name = 'app_versions'
		  AND column_name IN ('idempotency_key', 'request_digest')`).Scan(&legacyColumns); err != nil || legacyColumns != 0 {
		t.Fatalf("legacy idempotency columns must be dropped, found %d: %v", legacyColumns, err)
	}

	// The registry invariants live in the database, not only in Go: the
	// system scope is unreachable for any writer that bypasses the service.
	const owner = "01999999-9999-7999-8999-999999999998"
	if _, err := conn.Exec(ctx, `
		INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'migration test', now())`, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	rejected := `INSERT INTO workos_core.app_versions (
		id, owner_user_id, app_id, version, scope, name,
		permissions, manifest_digest, canonical_manifest, created_at
	) VALUES (
		'01999999-9999-7999-8999-999999999999', '%s', 'app-one', '1.0.0',
		'system', 'App', '{}', 'sha256:%s', '{}', now()
	)`
	if _, err := conn.Exec(ctx, fmt.Sprintf(rejected, owner, repeat("a", 64))); err == nil {
		t.Fatal("system scope must be rejected by the database constraint")
	}
}

// TestAppRegistryMigration003BackfillsExistingVolumeData replays the upgrade
// path of the acceptance volume: a database holding 002-era rows (idempotency
// facts on app_versions) migrates forward through 003 with a correct backfill.
func TestAppRegistryMigration003BackfillsExistingVolumeData(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)

	// Prime the database exactly like a pre-003 volume: apply the 001 and
	// 002 bodies by hand and record their checksums so migrations.Run only
	// needs to execute 003 forward.
	execOn(t, dsn, `CREATE SCHEMA IF NOT EXISTS workos_meta;
		CREATE TABLE IF NOT EXISTS workos_meta.schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL
		)`)
	for _, name := range []string{"001_foundation.sql", "002_app_registry.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "internal", "platform", "migrations", "files", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		execOn(t, dsn, string(body))
		digest := sha256.Sum256(body)
		execOn(t, dsn, `INSERT INTO workos_meta.schema_migrations (name, checksum, applied_at)
			VALUES ($1, $2, now())`, name, hex.EncodeToString(digest[:]))
	}

	const (
		ownerA     = "01999999-9999-7999-8999-99999999999a"
		ownerB     = "01999999-9999-7999-8999-99999999999b"
		versionID  = "01999999-9999-7999-8999-99999999999c"
		versionIDB = "01999999-9999-7999-8999-99999999999d"
		legacyKey  = "legacy-volume-key"
	)
	requestDigest := "sha256:" + repeat("a", 64)
	manifestDigest := "sha256:" + repeat("b", 64)
	// The deployment schema allows exactly one owner user; this scratch
	// database relaxes only that deployment index so the mapping's own
	// owner-scoped constraints can be proven against two owners.
	execOn(t, dsn, `DROP INDEX workos_core.users_single_owner_idx`)
	execOn(t, dsn, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'volume owner', now()), ($2, 'owner', 'second owner', now())`, ownerA, ownerB)
	legacyInsert := `INSERT INTO workos_core.app_versions (
		id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
		permissions, manifest_digest, canonical_manifest, created_at
	) VALUES (
		'%s', '%s', '%s', '%s', 'legacy-app', '1.0.0', 'user', 'Legacy App',
		'{}', '%s', '{}', now()
	)`
	execOn(t, dsn, fmt.Sprintf(legacyInsert, versionID, ownerA, legacyKey, requestDigest, manifestDigest))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("forward migration over 002-era data: %v", err)
	}

	// The legacy key was backfilled into the authoritative mapping and points
	// at the original immutable version.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	var mappedDigest, mappedVersion string
	if err := conn.QueryRow(ctx, `
		SELECT request_digest, app_version_id
		FROM workos_core.app_registration_requests
		WHERE owner_user_id = $1 AND idempotency_key = $2`, ownerA, legacyKey).Scan(&mappedDigest, &mappedVersion); err != nil {
		t.Fatalf("legacy key was not backfilled: %v", err)
	}
	if mappedDigest != requestDigest || mappedVersion != versionID {
		t.Fatalf("backfill mapped wrong facts: digest=%s version=%s", mappedDigest, mappedVersion)
	}

	// A different owner may hold the same key and the same (app, version):
	// owner isolation is part of the schema, not the service.
	execOn(t, dsn, fmt.Sprintf(`INSERT INTO workos_core.app_versions (
		id, owner_user_id, app_id, version, scope, name,
		permissions, manifest_digest, canonical_manifest, created_at
	) VALUES (
		'%s', '%s', 'legacy-app', '1.0.0', 'user', 'Second Owner App',
		'{}', '%s', '{}', now()
	)`, versionIDB, ownerB, "sha256:"+repeat("c", 64)))
	execOn(t, dsn, `INSERT INTO workos_core.app_registration_requests (
		owner_user_id, idempotency_key, request_digest, app_version_id, created_at
	) VALUES ($1, $2, $3, $4, now())`, ownerB, legacyKey, requestDigest, versionIDB)

	// The mapping's composite foreign key fails closed: a key can never be
	// bound to another owner's immutable version.
	execFails(t, dsn, `INSERT INTO workos_core.app_registration_requests (
		owner_user_id, idempotency_key, request_digest, app_version_id, created_at
	) VALUES ($1, 'cross-owner-key', $2, $3, now())`, ownerB, requestDigest, versionID)
	// The primary key forbids a second request under the same owner+key.
	execFails(t, dsn, `INSERT INTO workos_core.app_registration_requests (
		owner_user_id, idempotency_key, request_digest, app_version_id, created_at
	) VALUES ($1, $2, $3, $4, now())`, ownerA, legacyKey, requestDigest, versionID)
	// Malformed digests never enter the mapping.
	execFails(t, dsn, `INSERT INTO workos_core.app_registration_requests (
		owner_user_id, idempotency_key, request_digest, app_version_id, created_at
	) VALUES ($1, 'bad-digest-key', 'not-a-digest', $2, now())`, ownerA, versionID)
}

func withDatabase(rawURL, database string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func repeat(value string, count int) string {
	result := make([]byte, 0, count)
	for len(result) < count {
		result = append(result, value...)
	}
	return string(result[:count])
}
