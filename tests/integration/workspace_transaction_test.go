//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	app "github.com/yangtao121/workos/internal/indexer/application"
	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestWorkspaceTransactionalConvergence(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	generator := ids.UUIDv7{}
	repository := func() *app.ModelProjection {
		r, err := newModelProjection(pool, generator)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	repo := repository()
	if _, err := repo.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	source, err := repo.InsertWorkspaceSource(ctx, ports.WorkspaceSource{
		ID: generator.New(), OwnerUserID: generator.New(), ProjectID: generator.New(), RootPath: "/fixture/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := func(name, content string) ports.MountFile {
		digest := sha256.Sum256([]byte(content))
		return ports.MountFile{RelPath: name, Title: name, Content: []byte(content), Digest: "sha256:" + hex.EncodeToString(digest[:]), SourceID: domain.WorkspaceSourceID(source.OwnerUserID, source.ProjectID, name)}
	}
	source, _, _, err = repo.ConvergeWorkspacePass(ctx, source, []ports.MountFile{file("old.md", "previous fixture")}, 0, generator.New, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var state string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
   'documents', (SELECT jsonb_agg(to_jsonb(d) ORDER BY source_id) FROM workos_index.documents d),
   'receipts', (SELECT jsonb_agg(to_jsonb(r) ORDER BY publication_id) FROM workos_index.publication_receipts r),
   'cursors', (SELECT jsonb_agg(to_jsonb(c) ORDER BY worker_id) FROM workos_index.consumer_state c),
   'sources', (SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM workos_index.workspace_sources s))::text`).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	baseline := snapshot()
	candidates := []ports.MountFile{file("a.md", "first fixture"), file("b.md", "second fixture")}
	for _, failure := range []struct{ name, table, event, condition string }{
		{"second document", "documents", "INSERT", "NEW.title = 'b.md'"},
		{"source status", "workspace_sources", "UPDATE", "true"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			statement := fmt.Sprintf(`CREATE FUNCTION workos_index.fail_workspace_pass() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$;
    CREATE TRIGGER fail_workspace_pass BEFORE %s ON workos_index.%s FOR EACH ROW EXECUTE FUNCTION workos_index.fail_workspace_pass()`, failure.condition, failure.event, failure.table)
			if _, err := pool.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
			_, applied, deleted, err := repo.ConvergeWorkspacePass(ctx, source, candidates, 2, generator.New, time.Now().UTC())
			if err == nil || applied != 0 || deleted != 0 {
				t.Fatalf("failure reported applied=%d deleted=%d: %v", applied, deleted, err)
			}
			if got := snapshot(); got != baseline {
				t.Fatal("failed pass changed documents, receipts, cursor or source status")
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER fail_workspace_pass ON workos_index.%s; DROP FUNCTION workos_index.fail_workspace_pass()`, failure.table)); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A recreated service/repository can retry the whole unchanged source.
	repo = repository()
	updated, applied, deleted, err := repo.ConvergeWorkspacePass(ctx, source, candidates, 2, generator.New, time.Now().UTC())
	if err != nil || applied != 2 || deleted != 1 || updated.SkippedCount != 2 || !updated.UpdatedAt.After(source.UpdatedAt) {
		t.Fatalf("retry applied=%d deleted=%d: %v", applied, deleted, err)
	}
	committed := snapshot()
	if _, _, _, err := repo.ConvergeWorkspacePass(ctx, source, nil, 0, generator.New, time.Now().UTC()); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("stale deletion pass: %v", err)
	}
	if _, err := repo.SetWorkspaceSourceStatus(ctx, source, domain.WorkspaceDegraded, domain.DegradedMountMissing, time.Now().UTC()); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("late degradation: %v", err)
	}
	if snapshot() != committed {
		t.Fatal("stale outcomes changed committed facts")
	}

	// All workers scanned one version. Exactly one transaction may commit.
	source = updated
	outcomes := make(chan error, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < cap(outcomes); i++ {
		wg.Go(func() {
			<-start
			_, _, _, err := repo.ConvergeWorkspacePass(ctx, source, candidates, 0, generator.New, time.Now().UTC())
			outcomes <- err
		})
	}
	close(start)
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrWorkspaceConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent commits = %d, want 1", successes)
	}
	source, err = repo.GetWorkspaceSource(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Cancellation while waiting for the source lock is also side-effect free.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM workos_index.workspace_sources WHERE id = $1 FOR UPDATE`, source.ID); err != nil {
		t.Fatal(err)
	}
	committed = snapshot()
	canceled, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, _, _, err = repo.ConvergeWorkspacePass(canceled, source, nil, 0, generator.New, time.Now().UTC())
	cancel()
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("canceled pass committed")
	}
	if snapshot() != committed {
		t.Fatal("canceled pass changed facts")
	}

	rebound, err := repo.InsertWorkspaceSource(ctx, ports.WorkspaceSource{ID: generator.New(), OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID, RootPath: "/fixture/rebound"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.ConvergeWorkspacePass(ctx, source, nil, 0, generator.New, time.Now().UTC()); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("old binding: %v", err)
	}
	stopped, err := repo.SetWorkspaceSourceStatus(ctx, rebound, domain.WorkspaceStopped, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.ConvergeWorkspacePass(ctx, stopped, nil, 0, generator.New, time.Now().UTC()); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("stopped source: %v", err)
	}
}

// A project tombstone is terminal even when an admitted write is in flight.
func TestWorkspaceConcurrentArchive(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	generator := ids.UUIDv7{}
	repo, err := newModelProjection(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		now := time.Now().UTC()
		sum := sha256.Sum256([]byte("archive race fixture"))
		source := ports.ResolvedSource{
			Verdict: "resolved", Operation: "workspace.upsert", OwnerUserID: generator.New(), ProjectID: generator.New(),
			ArtifactID: generator.New(), ArtifactType: "workspace.text.v1", Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Title: "race.md", Content: []byte("archive race fixture"), CreatedAt: now, PublicationID: generator.New(), OccurredAt: now,
		}
		archive := source
		archive.Operation = "project.tombstone"
		archive.PublicationID = generator.New()
		results := make(chan error, 2)
		start := make(chan struct{})
		go func() {
			<-start
			results <- repo.ApplyResolvedSource(ctx, source, domain.OutcomeApplied, source.Digest, now)
		}()
		go func() {
			<-start
			results <- repo.ApplyResolvedSource(ctx, archive, domain.OutcomeTombstoned, archive.Digest, now)
		}()
		close(start)
		for j := 0; j < 2; j++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		var live int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents WHERE project_id = $1 AND tombstoned_at IS NULL`, source.ProjectID).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live != 0 {
			t.Fatal("concurrent write resurrected archived project")
		}
	}
}
