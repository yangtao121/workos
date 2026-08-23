//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// TestAppRegistryMigrationsFromEmptyDatabase proves 002_app_registry.sql
// applies on a pristine PostgreSQL 18 database, complementing the forward
// migration the bootstrap service performs on the existing acceptance volume.
func TestAppRegistryMigrationsFromEmptyDatabase(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	scratch := fmt.Sprintf("workos_migration_test_%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, withDatabase(databaseURL, "postgres"))
	if err != nil {
		t.Skipf("postgres is not reachable for the migration check: %v", err)
	}
	defer admin.Close(context.Background()) //nolint:errcheck
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s`, scratch)); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	defer admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %s WITH (FORCE)`, scratch)) //nolint:errcheck

	if err := migrations.Run(ctx, withDatabase(databaseURL, scratch)); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}

	scratchConn, err := pgx.Connect(ctx, withDatabase(databaseURL, scratch))
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer scratchConn.Close(context.Background()) //nolint:errcheck

	rows, err := scratchConn.Query(ctx, `SELECT name FROM workos_meta.schema_migrations ORDER BY name`)
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

	var tableExists bool
	if err := scratchConn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'workos_core' AND table_name = 'app_versions'
		)`).Scan(&tableExists); err != nil || !tableExists {
		t.Fatalf("app_versions table missing after migration: %v %v", err, tableExists)
	}

	// The registry invariants live in the database, not only in Go: the
	// system scope is unreachable for any writer that bypasses the service.
	const owner = "01999999-9999-7999-8999-999999999998"
	if _, err := scratchConn.Exec(ctx, `
		INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'migration test', now())`, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	rejected := fmt.Sprintf(`INSERT INTO workos_core.app_versions (
		id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
		permissions, manifest_digest, canonical_manifest, created_at
	) VALUES (
		'01999999-9999-7999-8999-999999999999', '%s', 'k', 'sha256:%s', 'app-one', '1.0.0',
		'system', 'App', '{}', 'sha256:%s', '{}', now()
	)`, owner, repeat("a", 64), repeat("b", 64))
	if _, err := scratchConn.Exec(ctx, rejected); err == nil {
		t.Fatal("system scope must be rejected by the database constraint")
	}
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
