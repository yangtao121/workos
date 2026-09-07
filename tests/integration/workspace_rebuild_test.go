//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/adapters/localmount"
	app "github.com/yangtao121/workos/internal/indexer/application"
	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

func TestWorkspaceSurvivesRebuild(t *testing.T) {
	f := newRebuildFixture(t)
	ctx := context.Background()
	if _, err := f.proj.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ingestor, err := app.NewWorkspaceIngestor(f.proj, localmount.NewWalker(), f.ids.New)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeWorkspaceFile(t, root, "notes.md", "workspace preservation fixture")
	project := "01999999-9999-7999-8999-000000000942"
	source, err := ingestor.Register(ctx, f.owner, project, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	search := app.NewSearchServiceForTest(f.proj)
	before, err := search.Search(ctx, app.SearchInput{OwnerUserID: f.owner, ProjectID: project, RawQuery: "preservation", SourceType: domain.SourceWorkspaceFile, PageSize: 10})
	if err != nil || len(before.Page.Hits) != 1 {
		t.Fatalf("initial workspace search: %v", err)
	}
	executor, _ := f.buildExecutor(t)
	job, _, err := executor.Start(ctx, app.RebuildRequest{Scope: "all", IdempotencyKey: "workspace-preservation"})
	if err != nil {
		t.Fatal(err)
	}
	f.driveToCompletionWithRestartEveryPass(t, ctx, job.ID)
	after, err := search.Search(ctx, app.SearchInput{OwnerUserID: f.owner, ProjectID: project, RawQuery: "preservation", SourceType: domain.SourceWorkspaceFile, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Page.Hits) != 1 || after.Page.Hits[0].ArtifactID != before.Page.Hits[0].ArtifactID || after.Page.Hits[0].Digest != before.Page.Hits[0].Digest {
		t.Fatal("rebuild lost the indexed workspace document")
	}
	// A second rebuild receives live edits while its review snapshot runs.
	executor, _ = f.buildExecutor(t)
	job, _, err = executor.Start(ctx, app.RebuildRequest{Scope: "all", IdempotencyKey: "workspace-live-rebuild"})
	if err != nil {
		t.Fatal(err)
	}
	advance := func(state string) {
		t.Helper()
		for i := 0; i < 30; i++ {
			current, err := executor.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == state {
				return
			}
			if _, err := executor.RunPass(ctx); err != nil {
				t.Fatal(err)
			}
		}
		t.Fatalf("never reached %s", state)
	}
	advance("snapshotting")
	if err := os.Remove(filepath.Join(root, "notes.md")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "next.md", "preservation replacement fixture")
	writeWorkspaceFile(t, root, "transient.md", "preservation transient fixture")
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	advance("promoting")
	if err := os.Remove(filepath.Join(root, "transient.md")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "late.md", "preservation late fixture")
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	old, err := f.proj.ActiveGenerationID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Hold a real workspace transaction at publication admission. Promotion
	// must wait for its shared generation lock and cannot lose the new file.
	writeWorkspaceFile(t, root, "racing.md", "preservation concurrent fixture")
	entered, release := make(chan struct{}), make(chan struct{})
	var admission, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	concurrent, err := app.NewWorkspaceIngestor(f.proj, localmount.NewWalker(), func() string {
		admission.Do(func() { close(entered); <-release })
		return f.ids.New()
	})
	if err != nil {
		t.Fatal(err)
	}
	syncDone := make(chan error, 1)
	go func() { _, err := concurrent.Sync(ctx, source.ID); syncDone <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("workspace transaction never admitted")
	}
	store, err := postgres.NewRebuildStore(f.pool, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, promotionErr := store.CompletePromotion(waiting, job.ID, job.TargetGeneration, old, time.Now().UTC())
	cancel()
	unblock()
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	if promotionErr == nil {
		t.Fatal("promotion bypassed the in-flight workspace transaction")
	}
	// A stale target row is repaired from the active snapshot only when the
	// pointer swap commits. Failure during copy rolls back clearing the target.
	execScratch(t, f.pool, `UPDATE workos_index.documents SET content = 'stale target fixture' WHERE projection_generation = $1 AND source_type = 'workspace.file.v1' AND tombstoned_at IS NULL`, job.TargetGeneration)
	execScratch(t, f.pool, `CREATE FUNCTION workos_index.reject_workspace_copy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.source_type = 'workspace.file.v1' THEN RAISE EXCEPTION 'fixture copy failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_workspace_copy BEFORE INSERT ON workos_index.documents FOR EACH ROW EXECUTE FUNCTION workos_index.reject_workspace_copy()`)
	store, err = postgres.NewRebuildStore(f.pool, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePromotion(ctx, job.ID, job.TargetGeneration, old, time.Now().UTC()); err == nil {
		t.Fatal("copy failure unexpectedly promoted")
	}
	current, err := f.proj.ActiveGenerationID(ctx)
	if err != nil || current != old {
		t.Fatalf("failed promotion changed active: %v", err)
	}
	var retained int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents WHERE projection_generation = $1 AND content = 'stale target fixture'`, job.TargetGeneration).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 3 {
		t.Fatalf("failed copy removed target facts: %d", retained)
	}
	execScratch(t, f.pool, `DROP TRIGGER reject_workspace_copy ON workos_index.documents; DROP FUNCTION workos_index.reject_workspace_copy()`)
	f.driveToCompletionWithRestartEveryPass(t, ctx, job.ID)
	assertLive := func(want int) {
		t.Helper()
		result, err := search.Search(ctx, app.SearchInput{OwnerUserID: f.owner, ProjectID: project, RawQuery: "preservation", SourceType: domain.SourceWorkspaceFile, PageSize: 10})
		if err != nil || len(result.Page.Hits) != want {
			t.Fatalf("live workspace count: %d, want %d: %v", len(result.Page.Hits), want, err)
		}
		for _, hit := range result.Page.Hits {
			if hit.Title == "notes.md" || strings.Contains(hit.Excerpt, "stale target") {
				t.Fatal("deleted or stale workspace content survived promotion")
			}
		}
	}
	assertLive(3)
	// A response-loss replay must not copy from its already retired source or
	// clear later edits to the now-active target.
	writeWorkspaceFile(t, root, "after.md", "preservation after promotion fixture")
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	store, err = postgres.NewRebuildStore(f.pool, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompletePromotion(ctx, job.ID, job.TargetGeneration, old, time.Now().UTC()); err != nil || !ok {
		t.Fatalf("promotion replay: %v", err)
	}
	assertLive(4)

}

func TestWorkspaceRebuildCopyLimits(t *testing.T) {
	for _, budget := range []struct {
		name          string
		copies, bytes int
	}{
		{"documents", domain.WorkspaceMaxRebuildDocuments, 8},
		{"bytes", domain.WorkspaceMaxRebuildBytes / domain.WorkspaceMaxFileBytes, domain.WorkspaceMaxFileBytes},
	} {
		t.Run(budget.name, func(t *testing.T) {
			f := newRebuildFixture(t)
			ctx := context.Background()
			active, err := f.proj.EnsureBootstrapGeneration(ctx, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			ingestor, err := app.NewWorkspaceIngestor(f.proj, localmount.NewWalker(), f.ids.New)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			writeWorkspaceFile(t, root, "base.md", "preservation fixture")
			source, err := ingestor.Register(ctx, f.owner, "01999999-9999-7999-8999-000000000942", root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ingestor.Sync(ctx, source.ID); err != nil {
				t.Fatal(err)
			}
			// Budget-only projection fixtures avoid thousands of filesystem writes;
			// promotion must reject both dimensions before any pointer change.
			execScratch(t, f.pool, `INSERT INTO workos_index.documents (
    projection_generation, owner_user_id, project_id, source_type, source_id, source_digest,
    artifact_type, title, content, source_created_at, last_publication_id, source_operation,
    indexed_at, updated_at, embedding)
    SELECT d.projection_generation, d.owner_user_id, d.project_id, d.source_type,
     ('01999999-1234-7abc-8abc-' || lpad(to_hex(n), 12, '0'))::uuid, d.source_digest,
     d.artifact_type, 'copy.md', $3, d.source_created_at, d.last_publication_id,
     d.source_operation, d.indexed_at, d.updated_at, d.embedding
    FROM workos_index.documents d CROSS JOIN generate_series(1, $2::integer) AS n
    WHERE d.projection_generation = $1 AND d.source_type = 'workspace.file.v1'`, active, budget.copies, strings.Repeat("fixture ", budget.bytes/8))
			executor, _ := f.buildExecutor(t)
			job, _, err := executor.Start(ctx, app.RebuildRequest{Scope: "all", IdempotencyKey: "workspace-copy-limit"})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 30; i++ {
				if _, err := executor.RunPass(ctx); err != nil {
					t.Fatal(err)
				}
				job, err = executor.GetJob(ctx, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				if job.State == "failed" {
					break
				}
			}
			if job.State != "failed" || job.FailureCategory != "workspace-copy-limit" {
				t.Fatalf("copy budget outcome: %s/%s", job.State, job.FailureCategory)
			}
			current, err := f.proj.ActiveGenerationID(ctx)
			if err != nil || current != active {
				t.Fatalf("copy limit changed active: %v", err)
			}
		})
	}
}

func TestRebuildPreservesHybridReviewRanking(t *testing.T) {
	f := newRebuildFixture(t)
	ctx := context.Background()
	if _, err := f.proj.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	feed := &portsCoreFeedAdapter{fake: &fakeRebuildFeed{pool: f.pool}, proj: f.proj}
	if err := app.Reconcile(ctx, feed, f.proj, f.ids, 100); err != nil {
		t.Fatal(err)
	}
	search := app.NewSearchServiceForTest(f.proj)
	input := app.SearchInput{OwnerUserID: f.owner, ProjectID: "01999999-9999-7999-8999-000000000942", RawQuery: "alpha unmatchedtoken", SourceType: domain.SourceReviewArtifact, PageSize: 50}
	before, err := search.SearchHybrid(ctx, input)
	if err != nil || len(before.Page.Hits) == 0 {
		t.Fatalf("initial hybrid recall: %v", err)
	}
	executor, _ := f.buildExecutor(t)
	job, _, err := executor.Start(ctx, app.RebuildRequest{Scope: "all", IdempotencyKey: "hybrid-review-golden"})
	if err != nil {
		t.Fatal(err)
	}
	f.driveToCompletionWithRestartEveryPass(t, ctx, job.ID)
	after, err := search.SearchHybrid(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Page.Hits, after.Page.Hits) {
		t.Fatal("rebuild changed hybrid review scores or recall")
	}
	var missing int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents WHERE projection_generation = $1 AND source_type = 'artifact.review.v1' AND (embedding IS NULL OR cardinality(embedding) <> $2)`, job.TargetGeneration, domain.EmbeddingDimensions).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("rebuilt review vectors missing: %d", missing)
	}
}

func TestRebuildSnapshotRespectsProjectArchive(t *testing.T) {
	f := newRebuildFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	generation, err := f.proj.EnsureBootstrapGeneration(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewRebuildStore(f.pool, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("snapshot archive fixture")
	sum := sha256.Sum256(content)
	effect := app.SnapshotEffect{
		OwnerUserID: f.owner, ProjectID: "01999999-9999-7999-8999-000000000942",
		ArtifactID: f.ids.New(), ArtifactType: "document.markdown.v1", Digest: "sha256:" + hex.EncodeToString(sum[:]),
		CreatedAt: now, Title: "Snapshot fixture", Content: content, PublicationID: f.ids.New(),
	}
	// Content was resolved before this project archive committed.
	if err := f.proj.ApplyResolvedSource(ctx, ports.ResolvedSource{
		Operation: "project.tombstone", OwnerUserID: effect.OwnerUserID, ProjectID: effect.ProjectID,
		PublicationID: f.ids.New(), OccurredAt: now,
	}, domain.OutcomeTombstoned, effect.Digest, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.ApplySnapshotSource(ctx, effect, generation, effect.Digest, now); err != nil {
			t.Fatal(err)
		}
	}
	var live int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents WHERE projection_generation=$1 AND source_id=$2 AND tombstoned_at IS NULL`, generation, effect.ArtifactID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatal("late rebuild snapshot resurrected an archived project")
	}
	var outcome string
	if err := f.pool.QueryRow(ctx, `SELECT outcome FROM workos_index.publication_receipts WHERE projection_generation=$1 AND publication_id=$2`, generation, effect.PublicationID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != domain.OutcomeTombstoned {
		t.Fatalf("snapshot receipt outcome = %s", outcome)
	}
}
