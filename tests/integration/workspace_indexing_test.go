//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/indexer/adapters/localmount"
	indexerpostgres "github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	indexerports "github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// TestWorkspaceIndexing proves the ADR-0017 §4 workspace slice against the
// real mount walker and projection repository: owner-bound registration with
// symlink-escape rejection, bounded ingestion with honest skip categories,
// convergent upsert/tombstone passes, explicit degraded mounts, and project
// archive dominance over workspace documents.
func TestWorkspaceIndexing(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	generator := ids.UUIDv7{}
	projection, err := indexerpostgres.New(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := projection.EnsureBootstrapGeneration(ctx, now); err != nil {
		t.Fatalf("bootstrap generation: %v", err)
	}
	ingestor, err := indexerapp.NewWorkspaceIngestor(projection, localmount.NewWalker(), generator.New)
	if err != nil {
		t.Fatal(err)
	}
	owner := "01999999-9999-7999-8999-000000000d01"
	project := "01999999-9999-7999-8999-000000000d02"
	search := indexerapp.NewSearchServiceForTest(projection)

	mount := t.TempDir()
	writeWorkspaceFile(t, mount, "notes.md", "# Notes\n\nThe workspace rollout pacing handbook for the indexer.\n")
	writeWorkspaceFile(t, mount, "main.go", "package main\n\nfunc main() {}\n")
	writeWorkspaceFile(t, mount, "config.json", "{\"workspace\": true}\n")
	writeWorkspaceFile(t, mount, ".git/config", "[core]\n")
	writeWorkspaceFile(t, mount, "node_modules/dep.js", "module.exports = 1;\n")
	writeWorkspaceFile(t, mount, ".env", "SECRET=never\n")
	writeWorkspaceFile(t, mount, "blob.bin", "\x00\x01\x02binary")
	// An allowed extension with a NUL payload exercises the binary sniff.
	writeWorkspaceFile(t, mount, "blob.md", "text with a \x00 nul payload\n")
	writeWorkspaceFile(t, mount, "huge.md", strings.Repeat("x", indexerdomain.WorkspaceMaxFileBytes+1)+"\n")
	writeWorkspaceFile(t, mount, "data.csv", "a,b,c\n")
	if err := os.Symlink("/etc/hostname", filepath.Join(mount, "escape.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A root that resolves through a symlink is rejected before any durable
	// state exists, and a relative root fails the grammar.
	if err := os.Symlink(t.TempDir(), filepath.Join(mount, "elsewhere")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ingestor.Register(ctx, owner, project, filepath.Join(mount, "elsewhere")); !errors.Is(err, indexerapp.ErrWorkspaceSymlinkEscape) {
		t.Fatalf("symlink escape must be rejected, got %v", err)
	}
	if _, err := ingestor.Register(ctx, owner, project, "relative/path"); !errors.Is(err, indexerdomain.ErrInvalid) {
		t.Fatalf("relative root must be invalid, got %v", err)
	}

	source, err := ingestor.Register(ctx, owner, project, mount)
	if err != nil {
		t.Fatalf("register mount: %v", err)
	}
	if source.Status != indexerdomain.WorkspaceActive {
		t.Fatalf("source status = %s, want active", source.Status)
	}

	first, err := ingestor.Sync(ctx, source.ID)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Applied != 3 {
		t.Fatalf("applied = %d, want 3 (notes.md, main.go, config.json)", first.Applied)
	}
	skipReasons := map[string]int{}
	for _, reason := range first.SkipReasons {
		skipReasons[reason]++
	}
	// extension: data.csv + blob.bin; binary: blob.md; symlink: escape.md
	// and the elsewhere/ probe; ignored: .git, node_modules, .env.
	if skipReasons[indexerdomain.SkipIgnored] != 3 ||
		skipReasons[indexerdomain.SkipBinary] != 1 ||
		skipReasons[indexerdomain.SkipOversize] != 1 ||
		skipReasons[indexerdomain.SkipExtension] != 2 ||
		skipReasons[indexerdomain.SkipSymlink] != 2 {
		t.Fatalf("skip categories drifted: %+v", skipReasons)
	}

	// The hybrid ranking serves workspace documents with honest provenance.
	found, err := search.SearchHybrid(ctx, indexerapp.SearchInput{
		OwnerUserID: owner, ProjectID: project, RawQuery: "workspace rollout pacing handbook", PageSize: 20,
	})
	if err != nil {
		t.Fatalf("hybrid workspace search: %v", err)
	}
	var notes *indexerdomain.SearchHit
	for i := range found.Page.Hits {
		hit := &found.Page.Hits[i]
		if hit.SourceType != indexerdomain.SourceWorkspaceFile || hit.ArtifactType != "workspace.text.v1" {
			t.Fatalf("hit provenance drifted: %+v", hit)
		}
		if hit.Title == "notes.md" {
			notes = hit
		}
	}
	if notes == nil {
		t.Fatal("notes.md never reached the hybrid ranking")
	}
	if !strings.Contains(notes.ContextRef, indexerdomain.SourceWorkspaceFile+":") {
		t.Fatalf("context ref grammar drifted: %q", notes.ContextRef)
	}
	var dims int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents, unnest(embedding) WHERE source_id = $1::uuid`, notes.ArtifactID).Scan(&dims); err != nil {
		t.Fatal(err)
	}
	if dims != indexerdomain.EmbeddingDimensions {
		t.Fatalf("workspace embedding dims = %d, want %d", dims, indexerdomain.EmbeddingDimensions)
	}

	// Convergence: modified content upserts, vanished files tombstone, new
	// files join — and the search projection follows exactly.
	writeWorkspaceFile(t, mount, "notes.md", "# Notes\n\nRevised: the greenhouse humidity ledger moved here.\n")
	writeWorkspaceFile(t, mount, "extra.txt", "fresh follow-up text\n")
	if err := os.Remove(filepath.Join(mount, "config.json")); err != nil {
		t.Fatal(err)
	}
	second, err := ingestor.Sync(ctx, source.ID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	// Every walked file is upserted (idempotent by digest); the vanished
	// config.json leaves the projection.
	if second.Applied != 3 || second.Tombstoned != 1 {
		t.Fatalf("second pass applied=%d tombstoned=%d, want 3/1", second.Applied, second.Tombstoned)
	}
	converged, err := search.SearchHybrid(ctx, indexerapp.SearchInput{
		OwnerUserID: owner, ProjectID: project, RawQuery: "workspace greenhouse humidity", PageSize: 20,
	})
	if err != nil {
		t.Fatalf("converged search: %v", err)
	}
	var revised bool
	for _, hit := range converged.Page.Hits {
		if hit.Title == "config.json" {
			t.Fatal("deleted file survived convergence in the projection")
		}
		if hit.Title == "notes.md" {
			// The revised query terms no longer lexically AND-match the
			// revised notes (no "workspace" token in it): the hit proves the
			// cosine half of the fusion carries the recall.
			if strings.Contains(hit.Excerpt, "rollout pacing") {
				t.Fatal("stale content survived the content upsert")
			}
			if strings.Contains(hit.Excerpt, "greenhouse humidity") {
				revised = true
			}
		}
	}
	if !revised {
		t.Fatalf("revised notes.md not recalled semantically: %+v", converged.Page.Hits)
	}

	// A vanished mount degrades explicitly and never pretends freshness.
	if err := os.RemoveAll(mount); err != nil {
		t.Fatal(err)
	}
	third, err := ingestor.Sync(ctx, source.ID)
	if err != nil {
		t.Fatalf("degraded sync: %v", err)
	}
	if third.Source.Status != indexerdomain.WorkspaceDegraded ||
		third.Source.DegradedReason != indexerdomain.DegradedMountMissing {
		t.Fatalf("degraded state drifted: %+v", third.Source)
	}
	// A degraded sync stops ingestion: retrying against the missing mount
	// keeps returning the durable degraded state, never a fresh result.
	retry, err := ingestor.Sync(ctx, source.ID)
	if err != nil {
		t.Fatalf("degraded retry: %v", err)
	}
	if retry.Source.Status != indexerdomain.WorkspaceDegraded {
		t.Fatalf("degraded retry status = %s", retry.Source.Status)
	}

	// Rebinding the same scope reactivates the source.
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, mount, "revived.md", "the workspace mount is revived\n")
	reactivated, err := ingestor.Register(ctx, owner, project, mount)
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if reactivated.ID != source.ID || reactivated.Status != indexerdomain.WorkspaceActive {
		t.Fatalf("rebind drifted: %+v", reactivated)
	}
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatalf("revival sync: %v", err)
	}

	// Project archive stays terminal: workspace documents leave the search
	// projection with the existing tombstone semantics.
	occurred := time.Now().UTC()
	if err := projection.ApplyResolvedSource(ctx, indexerports.ResolvedSource{
		Verdict: "tombstoned", Operation: "project.tombstone",
		OwnerUserID: owner, ProjectID: project,
		PublicationID: "01999999-9999-7999-8999-000000000d10", OccurredAt: occurred,
	}, indexerdomain.OutcomeTombstoned, "sha256:"+strings.Repeat("d", 64), occurred); err != nil {
		t.Fatalf("project tombstone: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		archived, searchErr := search.SearchHybrid(ctx, indexerapp.SearchInput{
			OwnerUserID: owner, ProjectID: project, RawQuery: "workspace", PageSize: 20,
		})
		if searchErr != nil {
			t.Fatalf("archived search: %v", searchErr)
		}
		if len(archived.Page.Hits) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("archived project retained %d workspace hits", len(archived.Page.Hits))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func writeWorkspaceFile(t *testing.T, mount, relPath, content string) {
	t.Helper()
	full := filepath.Join(mount, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
