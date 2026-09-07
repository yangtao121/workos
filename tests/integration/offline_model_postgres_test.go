//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/indexer/adapters/localembedding"
	"github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	"github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// Real pinned inference, persisted pgvector ranking, and snapshot regeneration.
// This opt-in test never substitutes the repository fixture embedding model.
func TestOfflineModelPostgres(t *testing.T) {
	directory := os.Getenv("WORKOS_LOCAL_EMBEDDING_MODEL_DIR")
	if directory == "" {
		t.Skip("pinned offline model directory required")
	}
	ctx := context.Background()
	worker, err := filepath.Abs("../../internal/indexer/adapters/localembedding/worker.py")
	if err != nil {
		t.Fatal(err)
	}
	model, err := localembedding.New(ctx, localembedding.Config{PythonPath: "/usr/local/bin/python3", WorkerPath: worker, ModelDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	generator := ids.UUIDv7{}
	raw, err := postgres.New(pool, generator, model.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	projection, err := application.NewModelProjection(raw, model)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	active, err := projection.EnsureBootstrapGeneration(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	owner, project := generator.New(), generator.New()
	docs := []struct{ title, content, id, pub string }{
		{title: "Hardware security", content: "Store private credentials in a hardware security key and require physical presence before authentication."},
		{title: "番茄种植", content: "温室番茄需要充足阳光、排水良好的土壤和适量浇水。"},
		{title: "Crash recovery", content: "After a process crashes, resume from the last durable checkpoint and replay committed events without duplication."},
	}
	for i := range docs {
		docs[i].id, docs[i].pub = generator.New(), generator.New()
		applySemanticDocument(t, ctx, projection, owner, project, docs[i].id, docs[i].pub, docs[i].title, docs[i].content, now.Add(-time.Hour), now)
	}
	search := application.NewSearchServiceForTest(projection)
	questions := []struct {
		query string
		want  int
	}{
		{"如何安全保存私钥并要求本人确认？", 0},
		{"进程崩溃后怎样从持久化检查点恢复而不重复处理事件？", 2},
		{"How should I water tomatoes in a greenhouse?", 1},
	}
	var golden [][]domain.SearchHit
	for _, question := range questions {
		input := application.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: question.query, PageSize: 10}
		lexical, err := search.Search(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(lexical.Page.Hits) != 0 {
			t.Fatal("cross-language fixture unexpectedly matched lexically")
		}
		result, err := search.SearchHybrid(ctx, input)
		if err != nil || len(result.Page.Hits) == 0 || result.Page.Hits[0].ArtifactID != docs[question.want].id {
			t.Fatalf("cross-language recall %q: %+v %v", question.query, result, err)
		}
		golden = append(golden, result.Page.Hits)
	}
	// The snapshot path regenerates exactly the model vectors used for live ingest.
	target := generator.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_index.projection_generations(id,scope,status,created_at) VALUES($1,'all','building',$2)`, target, now); err != nil {
		t.Fatal(err)
	}
	rawRebuild, err := postgres.NewRebuildStore(pool, generator, model.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	rebuild, err := application.NewModelRebuildStore(rawRebuild, model)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		effect := application.SnapshotEffect{OwnerUserID: owner, ProjectID: project, ArtifactID: doc.id, ArtifactType: "document.markdown.v1", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(doc.content))), Title: doc.title, Content: []byte(doc.content), CreatedAt: now.Add(-time.Hour), PublicationID: doc.pub}
		if err := rebuild.ApplySnapshotSource(ctx, effect, target, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(doc.id))), now); err != nil {
			t.Fatal(err)
		}
	}
	var identical int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.documents a JOIN workos_index.documents b USING(owner_user_id,project_id,source_id) WHERE a.projection_generation=$1 AND b.projection_generation=$2 AND a.embedding=b.embedding AND a.embedding_model=b.embedding_model`, active, target).Scan(&identical); err != nil || identical != len(docs) {
		t.Fatalf("snapshot vectors differ: %d %v", identical, err)
	}
	// Simulate lost derived cache, close the child, then resume from stored text.
	if _, err := pool.Exec(ctx, `UPDATE workos_index.documents SET embedding=NULL,embedding_model=NULL`); err != nil {
		t.Fatal(err)
	}
	model.Close()
	restarted, err := localembedding.New(ctx, localembedding.Config{PythonPath: "/usr/local/bin/python3", WorkerPath: worker, ModelDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	projection, err = application.NewModelProjection(raw, restarted)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := projection.Backfill(ctx); err != nil || count != 2*len(docs) {
		t.Fatalf("real model backfill: %d %v", count, err)
	}
	search = application.NewSearchServiceForTest(projection)
	for i, question := range questions {
		result, err := search.SearchHybrid(ctx, application.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: question.query, PageSize: 10})
		if err != nil || !reflect.DeepEqual(result.Page.Hits, golden[i]) {
			t.Fatalf("restart changed model ranking: %v", err)
		}
	}
}
