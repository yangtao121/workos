//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/tests/fixtures/embedding"
)

func modelFixture(t *testing.T) (*pgxpool.Pool, *application.ModelProjection, string, string) {
	t.Helper()
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	projection, err := newModelProjection(pool, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = projection.EnsureBootstrapGeneration(ctx, now); err != nil {
		t.Fatal(err)
	}
	owner, project := "01999999-9999-7999-8999-000000000a01", "01999999-9999-7999-8999-000000000a02"
	applySemanticDocument(t, ctx, projection, owner, project, "01999999-9999-7999-8999-000000000a03", "01999999-9999-7999-8999-000000000a04", "checkpoint", "checkpoint durable replay", now.Add(-time.Hour), now)
	return pool, projection, owner, project
}

func TestModelBackfillRejectsConcurrentChanges(t *testing.T) {
	for _, change := range []string{"content", "publication", "archive", "retire", "cleanup"} {
		t.Run(change, func(t *testing.T) {
			pool, projection, _, _ := modelFixture(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE workos_index.documents SET embedding=NULL, embedding_model=NULL`); err != nil {
				t.Fatal(err)
			}
			snapshots, err := projection.MissingEmbeddings(ctx, embedding.Fingerprint, 8)
			if err != nil || len(snapshots) != 1 {
				t.Fatalf("snapshots=%d err=%v", len(snapshots), err)
			}
			statement := map[string]string{
				"content":     `UPDATE workos_index.documents SET content='newer content', source_digest='sha256:` + strings.Repeat("a", 64) + `'`,
				"publication": `UPDATE workos_index.documents SET last_publication_id='01999999-9999-7999-8999-000000000a05'`,
				"archive":     `UPDATE workos_index.documents SET tombstoned_at=indexed_at`,
				"retire":      `UPDATE workos_index.projection_generations SET status='retired', retired_at=now()`,
				"cleanup":     `DELETE FROM workos_index.documents`,
			}[change]
			if _, err := pool.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
			values, _ := embedding.Model{}.Document(ctx, "checkpoint\ncheckpoint durable replay")
			applied, err := projection.StoreEmbedding(ctx, snapshots[0], domain.ModelVector{Values: values, Fingerprint: embedding.Fingerprint})
			if err != nil || applied {
				t.Fatalf("stale backfill accepted: %t %v", applied, err)
			}
		})
	}
}

func TestModelMigrationPreservesProjectionAndResumesBackfill(t *testing.T) {
	pool, projection, owner, project := modelFixture(t)
	ctx := context.Background()
	// Reconstruct the pre-047 vector cache in this owned scratch database.
	if _, err := pool.Exec(ctx, `ALTER TABLE workos_index.documents
 DROP CONSTRAINT documents_embedding_identity, DROP CONSTRAINT documents_embedding_normalized,
 DROP COLUMN embedding_model, ALTER COLUMN embedding TYPE real[] USING embedding::real[];
 DELETE FROM workos_meta.schema_migrations WHERE name='047_indexer_model_vectors.sql'`); err != nil {
		t.Fatal(err)
	}
	facts := func() string {
		t.Helper()
		var value string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'documents',(SELECT jsonb_agg(to_jsonb(d)-'embedding'-'embedding_model') FROM workos_index.documents d),
 'receipts',(SELECT jsonb_agg(to_jsonb(r)) FROM workos_index.publication_receipts r),
 'cursors',(SELECT jsonb_agg(to_jsonb(c)) FROM workos_index.consumer_state c))::text`).Scan(&value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := facts()
	if err := migrations.Run(ctx, pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if before != facts() {
		t.Fatal("migration changed projection facts")
	}
	search := application.NewSearchServiceForTest(projection)
	input := application.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "checkpoint", PageSize: 10}
	if _, err := search.SearchHybrid(ctx, input); !errors.Is(err, ports.ErrEmbeddingUnavailable) {
		t.Fatalf("incomplete model accepted: %v", err)
	}
	// A new application instance resumes solely from the stored model identity.
	resumed, err := newModelProjection(pool, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := resumed.Backfill(ctx); err != nil || n != 1 {
		t.Fatalf("backfill %d %v", n, err)
	}
	if n, err := resumed.Backfill(ctx); err != nil || n != 0 {
		t.Fatalf("replayed backfill %d %v", n, err)
	}
	if before != facts() {
		t.Fatal("backfill changed projection facts")
	}
	page, err := search.SearchHybrid(ctx, input)
	if err != nil || len(page.Page.Hits) != 1 {
		t.Fatalf("restored search: %+v %v", page, err)
	}
}

func TestModelSearchRanksBeyondOldCandidateLimit(t *testing.T) {
	pool, projection, owner, project := modelFixture(t)
	ctx := context.Background()
	// 2,102 equal-score documents; the former Go candidate LIMIT 2000 silently
	// lost the last 102 and could not paginate a complete scope.
	if _, err := pool.Exec(ctx, `INSERT INTO workos_index.documents (
 projection_generation,owner_user_id,project_id,source_type,source_id,source_digest,
 artifact_type,title,content,source_created_at,last_publication_id,source_operation,indexed_at,updated_at,embedding,embedding_model)
 SELECT d.projection_generation,d.owner_user_id,d.project_id,d.source_type,
 ('01999999-9999-7999-8999-'||lpad(to_hex(i),12,'0'))::uuid,d.source_digest,
 d.artifact_type,d.title,d.content,d.source_created_at,d.last_publication_id,d.source_operation,d.indexed_at,d.updated_at,d.embedding,d.embedding_model
 FROM workos_index.documents d CROSS JOIN generate_series(10000,12100) i`); err != nil {
		t.Fatal(err)
	}
	search := application.NewSearchServiceForTest(projection)
	input := application.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "checkpoint", PageSize: 50}
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < 44; pageNumber++ {
		result, err := search.SearchHybrid(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range result.Page.Hits {
			if seen[hit.ArtifactID] {
				t.Fatal("duplicate page result")
			}
			seen[hit.ArtifactID] = true
		}
		input.PageToken = result.Page.NextPageToken
		if pageNumber == 0 && input.PageToken != "" {
			next := application.NewSearchServiceForTest(nextRecipeProjection{projection})
			if _, err := next.SearchHybrid(ctx, input); !errors.Is(err, domain.ErrInvalidPageToken) {
				t.Fatalf("old recipe token accepted: %v", err)
			}
		}
		if input.PageToken == "" {
			break
		}
	}
	if len(seen) != 2102 || input.PageToken != "" {
		t.Fatal(fmt.Sprintf("incomplete ranking: %d documents, continuation=%t", len(seen), input.PageToken != ""))
	}
}

func TestModelPromotionWaitsForCompleteVectors(t *testing.T) {
	f := newRebuildFixture(t)
	ctx := context.Background()
	active, err := f.proj.EnsureBootstrapGeneration(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	executor, _ := f.buildExecutor(t)
	job, _, err := executor.Start(ctx, application.RebuildRequest{Scope: "all", IdempotencyKey: "model-promotion-readiness"})
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 30 && job.State != "promoting"; pass++ {
		if _, err := executor.RunPass(ctx); err != nil {
			t.Fatal(err)
		}
		job, err = executor.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.State != "promoting" {
		t.Fatalf("unexpected state: %s", job.State)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workos_index.documents SET embedding=NULL,embedding_model=NULL WHERE projection_generation=$1`, job.TargetGeneration); err != nil {
		t.Fatal(err)
	}
	store, err := newModelRebuildStore(f.pool, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompletePromotion(ctx, job.ID, job.TargetGeneration, active, time.Now().UTC()); ok || !errors.Is(err, ports.ErrEmbeddingUnavailable) {
		t.Fatalf("incomplete promotion accepted: %t %v", ok, err)
	}
	if current, err := f.proj.ActiveGenerationID(ctx); err != nil || current != active {
		t.Fatalf("failed promotion moved pointer: %v", err)
	}
	for pass := 0; pass < 50; pass++ {
		n, err := f.proj.Backfill(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	f.driveToCompletionWithRestartEveryPass(t, ctx, job.ID)
	if current, err := f.proj.ActiveGenerationID(ctx); err != nil || current != job.TargetGeneration {
		t.Fatalf("ready generation not promoted: %v", err)
	}
}

// Changing the recipe must invalidate pagination before querying any vectors.
type nextRecipeProjection struct{ ports.ProjectionRepository }

func (nextRecipeProjection) ModelFingerprint() string { return "sha256:" + strings.Repeat("a", 64) }
