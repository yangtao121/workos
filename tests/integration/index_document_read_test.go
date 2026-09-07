//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/indexer/adapters/localmount"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestIndexSourceFilterAndExactSnapshotRead(t *testing.T) {
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
	now := time.Now().UTC()
	if _, err := repo.EnsureBootstrapGeneration(ctx, now); err != nil {
		t.Fatal(err)
	}
	owner, project := generator.New(), generator.New()
	mount := t.TempDir()
	writeWorkspaceFile(t, mount, "alpha.md", "# Design\nThe design notes for the first workspace file.")
	writeWorkspaceFile(t, mount, "beta.md", "# Design\nThe design notes for the second workspace file.")
	ingestor, err := indexerapp.NewWorkspaceIngestor(repo, localmount.NewWalker(), generator.New)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ingestor.Register(ctx, owner, project, mount)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	applySemanticDocument(t, ctx, repo, owner, project, generator.New(), generator.New(), "Design", "Design review artifact", now, now)
	search := indexerapp.NewSearchServiceForTest(repo)
	input := indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "design", PageSize: 1, SourceType: indexerdomain.SourceWorkspaceFile}
	first, err := search.SearchHybrid(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Page.Hits) != 1 || first.Page.Hits[0].SourceType != indexerdomain.SourceWorkspaceFile || first.Page.NextPageToken == "" {
		t.Fatalf("file filter failed: %+v", first)
	}
	input.PageToken = first.Page.NextPageToken
	second, err := search.SearchHybrid(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Page.Hits) != 1 || second.Page.NextPageToken != "" || second.Page.Hits[0].ArtifactID == first.Page.Hits[0].ArtifactID {
		t.Fatalf("pagination failed: %+v", second)
	}
	input.SourceType = indexerdomain.SourceReviewArtifact
	if _, err := search.SearchHybrid(ctx, input); !errors.Is(err, indexerdomain.ErrInvalidPageToken) {
		t.Fatalf("cross-filter token accepted: %v", err)
	}
	hit := first.Page.Hits[0]
	ref := indexerdomain.DocumentRead{OwnerUserID: owner, ProjectID: project, SourceID: hit.ArtifactID, SourceType: hit.SourceType, Digest: hit.Digest}
	doc, err := search.ReadDocument(ctx, ref)
	if err != nil || doc.Title != hit.Title || doc.Content == "" {
		t.Fatalf("read snapshot: %+v %v", doc, err)
	}
	foreign := ref
	foreign.OwnerUserID = generator.New()
	if _, err := search.ReadDocument(ctx, foreign); !errors.Is(err, indexerdomain.ErrNotFound) {
		t.Fatalf("foreign snapshot leaked: %v", err)
	}
	if err := os.Remove(filepath.Join(mount, hit.Title)); err != nil {
		t.Fatal(err)
	}
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := search.ReadDocument(ctx, ref); !errors.Is(err, indexerdomain.ErrNotFound) {
		t.Fatalf("deleted snapshot readable: %v", err)
	}
}
