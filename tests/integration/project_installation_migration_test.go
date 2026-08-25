//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
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

// TestProjectInstallationMigrationFromEmptyDatabase proves 004 applies on a
// pristine database and installs its invariants as real constraints.
func TestProjectInstallationMigrationFromEmptyDatabase(t *testing.T) {
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
// forward migration really ran on the persistent acceptance volume the
// gateway tests use, not only on scratch databases.
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
}

func newUUIDForTest(suffix int) string {
	return fmt.Sprintf("01999999-9999-7999-8999-%012d", suffix)
}
