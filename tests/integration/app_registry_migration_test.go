//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// scratchDatabaseURL resolves the admin DSN every scratch-database helper
// uses.
func scratchDatabaseURL() string {
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	return databaseURL
}

// connectScratchAdmin opens an admin connection for pg_database assertions,
// skipping the caller when postgres is unreachable.
func connectScratchAdmin(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, withDatabase(scratchDatabaseURL(), "postgres"))
	if err != nil {
		t.Skipf("postgres is not reachable for the migration check: %v", err)
	}
	return admin
}

// closeConn closes a scratch-side connection with its own bounded context so
// a stuck close can never block a test forever. Close errors here are not
// test failures: the connection's work was already verified and the database
// lifecycle itself is guarded by the exact-name cleanup.
func closeConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Close(ctx) //nolint:errcheck
}

// scratchAdminLifecycle bundles the fallible admin-connection operations the
// scratch helpers perform, so lifecycle tests can inject deterministic
// failures (a creation close that errors after CREATE DATABASE, a cleanup
// close that errors after the DROP) and record the contexts they ran under —
// no network flakiness, no shared-database pollution. The default lifecycle
// is the real pgx behavior.
type scratchAdminLifecycle struct {
	Connect func(ctx context.Context, dsn string) (*pgx.Conn, error)
	Exec    func(ctx context.Context, conn *pgx.Conn, statement string) error
	Close   func(ctx context.Context, conn *pgx.Conn) error
}

func realScratchAdminLifecycle() scratchAdminLifecycle {
	return scratchAdminLifecycle{
		Connect: pgx.Connect,
		Exec: func(ctx context.Context, conn *pgx.Conn, statement string) error {
			_, err := conn.Exec(ctx, statement)
			return err
		},
		Close: func(ctx context.Context, conn *pgx.Conn) error {
			return conn.Close(ctx)
		},
	}
}

// errScratchPostgresUnreachable marks an admin connect failure so the thin
// test wrapper can skip (instead of fail) when postgres is simply down.
var errScratchPostgresUnreachable = errors.New("postgres is not reachable for the migration check")

// scratchDatabase creates an isolated database and returns its DSN. The
// exact-name DROP cleanup is registered after the admin connect but before
// CREATE DATABASE executes — that is, before any fallible step that can
// follow a possibly-created database — so helper success, t.Fatal on CREATE
// or on the creation close, panic, and early return all drop precisely this
// round's database, and even an indeterminate CREATE error (server created,
// client saw a failure) is covered because the DROP is idempotent.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	dsn, err := provisionScratchDatabase(t, scratchDatabaseURL(), realScratchAdminLifecycle())
	if errors.Is(err, errScratchPostgresUnreachable) {
		t.Skipf("%v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// provisionScratchDatabase is the checkable core of scratchDatabase: it
// returns the failure instead of failing the test itself, so lifecycle tests
// can assert the error while the exact-name cleanup stays registered on t
// and therefore still executes.
func provisionScratchDatabase(t *testing.T, databaseURL string, lc scratchAdminLifecycle) (string, error) {
	t.Helper()
	scratch := fmt.Sprintf("workos_migration_test_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := lc.Connect(ctx, withDatabase(databaseURL, "postgres"))
	if err != nil {
		return "", fmt.Errorf("%w: %w", errScratchPostgresUnreachable, err)
	}
	t.Cleanup(func() {
		dropScratchDatabaseWithTest(t, databaseURL, scratch, lc)
	})
	if err := lc.Exec(ctx, admin, fmt.Sprintf(`CREATE DATABASE %s`, pgx.Identifier{scratch}.Sanitize())); err != nil {
		if closeErr := lc.Close(ctx, admin); closeErr != nil {
			return "", fmt.Errorf("create scratch database %s: %v (also failed closing the creation connection: %v)", scratch, err, closeErr)
		}
		return "", fmt.Errorf("create scratch database %s: %w", scratch, err)
	}
	if err := lc.Close(ctx, admin); err != nil {
		return "", fmt.Errorf("close scratch creation connection: %w", err)
	}
	return withDatabase(databaseURL, scratch), nil
}

// dropScratchDatabase removes exactly one scratch database by its generated,
// safely quoted name — never a wildcard and never another test's database —
// and reports every failure as a test error.
func dropScratchDatabase(t *testing.T, databaseURL, database string) {
	t.Helper()
	dropScratchDatabaseWithTest(t, databaseURL, database, realScratchAdminLifecycle())
}

func dropScratchDatabaseWithTest(t *testing.T, databaseURL, database string, lc scratchAdminLifecycle) {
	t.Helper()
	if err := dropScratchDatabaseWithLifecycle(databaseURL, database, lc); err != nil {
		t.Error(err)
	}
}

// dropScratchDatabaseWithLifecycle is the checkable core of the cleanup: it
// opens its own admin connection with a bounded context, drops with IF
// EXISTS so the pre-CREATE registration is harmless when CREATE never
// created anything, and closes the admin connection with the same bounded
// context, returning every failure instead of swallowing it.
func dropScratchDatabaseWithLifecycle(databaseURL, database string, lc scratchAdminLifecycle) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := lc.Connect(ctx, withDatabase(databaseURL, "postgres"))
	if err != nil {
		return fmt.Errorf("cleanup: connect to drop scratch database %s: %w", database, err)
	}
	var errs []error
	if err := lc.Exec(ctx, admin, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{database}.Sanitize())); err != nil {
		errs = append(errs, fmt.Errorf("cleanup: drop scratch database %s: %w", database, err))
	}
	if err := lc.Close(ctx, admin); err != nil {
		errs = append(errs, fmt.Errorf("cleanup: close admin connection for %s: %w", database, err))
	}
	return errors.Join(errs...)
}

// TestScratchDatabaseCleanupDropsCreatedDatabase guards the happy-path
// lifecycle: after the subtest that used the scratch database returns, its
// t.Cleanup has run, so the database must be gone. A regression that closes
// the cleanup connection when the helper returns (or swallows the DROP
// error) leaves the database behind and fails here. The failure paths are
// guarded separately, below.
func TestScratchDatabaseCleanupDropsCreatedDatabase(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, withDatabase(scratchDatabaseURL(), "postgres"))
	if err != nil {
		t.Skipf("postgres is not reachable for the migration check: %v", err)
	}
	defer closeConn(admin)

	var created string
	t.Run("uses a scratch database", func(t *testing.T) {
		dsn := scratchDatabase(t)
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse scratch dsn: %v", err)
		}
		created = strings.TrimPrefix(parsed.Path, "/")
		if !strings.HasPrefix(created, "workos_migration_test_") {
			t.Fatalf("unexpected scratch database name %q", created)
		}
		var exists bool
		if err := admin.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, created).Scan(&exists); err != nil || !exists {
			t.Fatalf("scratch database must exist while its test runs: %v %v", err, exists)
		}
	})
	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, created).Scan(&exists); err != nil {
		t.Fatalf("inspect scratch database after cleanup: %v", err)
	}
	if exists {
		t.Fatalf("scratch database %s was not dropped by the test cleanup", created)
	}
}

// syntheticCloseFailure is the deterministic error injected through the
// lifecycle seam; it never depends on network behavior or timing.
var syntheticCloseFailure = errors.New("synthetic scratch admin close failure")

// scratchNameFromCreateStatement extracts the internally generated, quoted
// database name from a recorded CREATE DATABASE statement.
func scratchNameFromCreateStatement(statement string) string {
	return strings.Trim(strings.TrimPrefix(statement, "CREATE DATABASE "), `"`)
}

// TestScratchDatabaseCleanupSurvivesCreationCloseFailure pins the failure
// path the happy-path guard cannot reach: CREATE DATABASE has succeeded, and
// then the creation connection's Close fails. The exact-name cleanup must
// already be registered when that error is reported — so the just-created
// database is still dropped — and every admin exec and close, including the
// ones inside the cleanup, must run on bounded contexts.
func TestScratchDatabaseCleanupSurvivesCreationCloseFailure(t *testing.T) {
	t.Parallel()
	verify := connectScratchAdmin(t)
	defer closeConn(verify)

	lc := realScratchAdminLifecycle()
	var (
		created    []string
		drops      []string
		execCtxs   []context.Context
		closeCtxs  []context.Context
		closeCalls int
	)
	realExec := lc.Exec
	lc.Exec = func(ctx context.Context, conn *pgx.Conn, statement string) error {
		switch {
		case strings.HasPrefix(statement, "CREATE DATABASE "):
			created = append(created, statement)
		case strings.HasPrefix(statement, "DROP DATABASE "):
			drops = append(drops, statement)
		}
		execCtxs = append(execCtxs, ctx)
		return realExec(ctx, conn, statement)
	}
	lc.Close = func(ctx context.Context, conn *pgx.Conn) error {
		closeCalls++
		closeCtxs = append(closeCtxs, ctx)
		if closeCalls == 1 {
			// The deterministic post-CREATE close failure under test. The
			// socket is still closed for real so the fake cannot leak the
			// connection it stands in for.
			conn.Close(ctx) //nolint:errcheck
			return syntheticCloseFailure
		}
		return conn.Close(ctx)
	}

	t.Run("creation close fails after CREATE", func(t *testing.T) {
		dsn, err := provisionScratchDatabase(t, scratchDatabaseURL(), lc)
		if !errors.Is(err, syntheticCloseFailure) {
			t.Fatalf("the creation close failure must be reported to the caller, got dsn=%q err=%v", dsn, err)
		}
	})
	if len(created) != 1 {
		t.Fatalf("expected exactly one CREATE DATABASE before the failing close, got %v", created)
	}
	name := scratchNameFromCreateStatement(created[0])
	expectedDrop := fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{name}.Sanitize())
	if len(drops) != 1 || drops[0] != expectedDrop {
		t.Fatalf("cleanup must drop exactly the created name %q: got %v, want [%s]", name, drops, expectedDrop)
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	var exists bool
	if err := verify.QueryRow(checkCtx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil || exists {
		t.Fatalf("scratch database %s leaked after the creation close failure: %v %v", name, err, exists)
	}
	for _, ctx := range append(execCtxs, closeCtxs...) {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Fatal("every scratch admin exec and close must use a bounded context")
		}
	}
}

// TestScratchDatabaseCleanupReportsFailuresWithBoundedContexts pins the
// cleanup's own lifecycle contract: the DROP executes on a freshly connected
// admin under a bounded context, the final admin close is bounded as well,
// and a close failure is returned to the caller (surfaced as a test error by
// the reporting wrapper) instead of being swallowed — while the exact-name
// DROP itself still happened.
func TestScratchDatabaseCleanupReportsFailuresWithBoundedContexts(t *testing.T) {
	t.Parallel()
	databaseURL := scratchDatabaseURL()
	admin := connectScratchAdmin(t)
	defer closeConn(admin)

	scratch := fmt.Sprintf("workos_migration_test_%d", time.Now().UnixNano())
	createCtx, createCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer createCancel()
	if _, err := admin.Exec(createCtx, fmt.Sprintf(`CREATE DATABASE %s`, pgx.Identifier{scratch}.Sanitize())); err != nil {
		t.Fatalf("create scratch database for the drop lifecycle probe: %v", err)
	}
	// Belt and braces: the probe database must disappear even if an
	// assertion below fails before the injected-lifecycle drop is reached.
	t.Cleanup(func() {
		dropScratchDatabase(t, databaseURL, scratch)
	})

	lc := realScratchAdminLifecycle()
	var (
		drops     []string
		execCtxs  []context.Context
		closeCtxs []context.Context
	)
	realExec := lc.Exec
	lc.Exec = func(ctx context.Context, conn *pgx.Conn, statement string) error {
		if strings.HasPrefix(statement, "DROP DATABASE ") {
			drops = append(drops, statement)
		}
		execCtxs = append(execCtxs, ctx)
		return realExec(ctx, conn, statement)
	}
	lc.Close = func(ctx context.Context, conn *pgx.Conn) error {
		closeCtxs = append(closeCtxs, ctx)
		conn.Close(ctx) //nolint:errcheck
		return syntheticCloseFailure
	}

	t.Run("drop with failing admin close", func(t *testing.T) {
		if err := dropScratchDatabaseWithLifecycle(databaseURL, scratch, lc); !errors.Is(err, syntheticCloseFailure) {
			t.Fatalf("the cleanup close failure must be reported to the caller, got %v", err)
		}
	})
	expectedDrop := fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{scratch}.Sanitize())
	if len(drops) != 1 || drops[0] != expectedDrop {
		t.Fatalf("cleanup must drop exactly the probed name: got %v, want [%s]", drops, expectedDrop)
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	var exists bool
	if err := admin.QueryRow(checkCtx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, scratch).Scan(&exists); err != nil || exists {
		t.Fatalf("scratch database %s leaked after the failing cleanup close: %v %v", scratch, err, exists)
	}
	if len(execCtxs) != 1 || len(closeCtxs) != 1 {
		t.Fatalf("expected exactly one bounded exec and one bounded close, got %d execs / %d closes", len(execCtxs), len(closeCtxs))
	}
	for _, ctx := range append(execCtxs, closeCtxs...) {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Fatal("cleanup exec and close must use bounded contexts")
		}
	}
}

func execOn(t *testing.T, dsn string, statement string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	defer closeConn(conn)
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
	defer closeConn(conn)
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
	defer closeConn(conn)

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
	defer closeConn(conn)
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
