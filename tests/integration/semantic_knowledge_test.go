//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	projectv1connect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	indexerpostgres "github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	indexerports "github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// TestSemanticKnowledgeHybridRepository proves the ADR-0017 semantic slice
// against the real projection repository on a scratch database: embeddings
// are computed and stored at ingest, the fused ranking is deterministic,
// documents without embeddings degrade to the lexical path, and a page token
// from one ranking never paginates the other.
func TestSemanticKnowledgeHybridRepository(t *testing.T) {
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

	owner := "01999999-9999-7999-8999-000000000c01"
	project := "01999999-9999-7999-8999-000000000c02"
	docs := []struct {
		id      string
		pub     string
		title   string
		body    string
		created time.Time
	}{
		{
			id: "01999999-9999-7999-8999-000000000c11", pub: "01999999-9999-7999-8999-000000000c21",
			title:   "Kubernetes rollout handbook",
			body:    "The kubernetes rollout pacing gate blocks a deploy when the canary error budget burns. Rollout pacing is the whole point of the kubernetes gate.",
			created: now.Add(-2 * time.Hour),
		},
		{
			id: "01999999-9999-7999-8999-000000000c12", pub: "01999999-9999-7999-8999-000000000c22",
			title:   "Garden journal",
			body:    "The garden journal cross-checks the kubernetes rollout pacing gate before each watering. Soil, mulch, compost temperatures, and greenhouse humidity follow weekly.",
			created: now.Add(-1 * time.Hour),
		},
	}
	for _, doc := range docs {
		applySemanticDocument(t, ctx, projection, owner, project, doc.id, doc.pub, doc.title, doc.body, doc.created, now)
	}

	// The write path must persist a 384-dimensional L2-normalized embedding.
	var dims int
	var norm float64
	if err := pool.QueryRow(ctx, `SELECT count(*), sqrt(sum(power(value, 2)))
		FROM workos_index.documents, unnest(embedding) AS value
		WHERE source_id = $1::uuid`, docs[0].id).Scan(&dims, &norm); err != nil {
		t.Fatalf("read stored embedding: %v", err)
	}
	if dims != indexerdomain.EmbeddingDimensions || norm < 0.999 || norm > 1.001 {
		t.Fatalf("stored embedding dims=%d norm=%v, want 384 and ~1", dims, norm)
	}

	search := indexerapp.NewSearchServiceForTest(projection)
	query := indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "kubernetes rollout pacing gate", PageSize: 20}
	var gardenFused float64

	hybrid, err := search.SearchHybrid(ctx, query)
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(hybrid.Page.Hits) != 2 {
		t.Fatalf("hybrid hits = %d, want 2", len(hybrid.Page.Hits))
	}
	if hybrid.Page.Hits[0].ArtifactID != docs[0].id {
		t.Fatalf("hybrid ranking put %q first", hybrid.Page.Hits[0].ArtifactID)
	}
	for _, hit := range hybrid.Page.Hits {
		// Fused scores live in (0,1]: 0.5 normalized lexical + 0.5 clamped cosine.
		if hit.Score <= 0 || hit.Score > 1 {
			t.Fatalf("fused score %v outside (0,1]", hit.Score)
		}
	}
	again, err := search.SearchHybrid(ctx, query)
	if err != nil {
		t.Fatalf("repeat hybrid search: %v", err)
	}
	for i := range again.Page.Hits {
		if again.Page.Hits[i].ArtifactID != hybrid.Page.Hits[i].ArtifactID ||
			again.Page.Hits[i].Score != hybrid.Page.Hits[i].Score {
			t.Fatalf("hybrid ranking is not deterministic: %+v vs %+v", again.Page.Hits, hybrid.Page.Hits)
		}
	}

	for _, hit := range hybrid.Page.Hits {
		if hit.ArtifactID == docs[1].id {
			gardenFused = hit.Score
		}
	}

	// Legacy rows without embeddings degrade honestly: the lexical path
	// keeps the document searchable and its fused score loses exactly the
	// semantic half.
	if _, err := pool.Exec(ctx, `UPDATE workos_index.documents SET embedding = NULL WHERE source_id = $1::uuid`, docs[1].id); err != nil {
		t.Fatal(err)
	}
	degraded, err := search.SearchHybrid(ctx, query)
	if err != nil {
		t.Fatalf("degraded hybrid search: %v", err)
	}
	if len(degraded.Page.Hits) != 2 || degraded.Page.Hits[1].ArtifactID != docs[1].id {
		t.Fatalf("degraded document lost from hybrid results: %+v", degraded.Page.Hits)
	}
	if degraded.Page.Hits[1].Score >= gardenFused {
		t.Fatalf("removing the embedding must lower the fused score: %v >= %v", degraded.Page.Hits[1].Score, gardenFused)
	}

	// A page token from one ranking never paginates the other.
	lexicalFirst, err := search.Search(ctx, indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "kubernetes rollout pacing gate", PageSize: 1})
	if err != nil {
		t.Fatalf("lexical page one: %v", err)
	}
	if lexicalFirst.Page.NextPageToken == "" {
		t.Fatal("lexical fixture must produce a second page token")
	}
	_, err = search.SearchHybrid(ctx, indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "kubernetes rollout pacing gate", PageSize: 1, PageToken: lexicalFirst.Page.NextPageToken})
	if !errors.Is(err, indexerdomain.ErrInvalidPageToken) {
		t.Fatalf("cross-ranking token must be rejected, got %v", err)
	}

	// The hybrid token chain walks every hit exactly once and stops without
	// a phantom page.
	fullOrder := hybrid.Page.Hits
	var walked []string
	token := ""
	pages := 0
	for {
		page, pageErr := search.SearchHybrid(ctx, indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "kubernetes rollout pacing gate", PageSize: 1, PageToken: token})
		if pageErr != nil {
			t.Fatalf("hybrid page walk: %v", pageErr)
		}
		if len(page.Page.Hits) != 1 {
			t.Fatalf("page walk returned %d hits", len(page.Page.Hits))
		}
		walked = append(walked, page.Page.Hits[0].ArtifactID)
		if page.Page.NextPageToken == "" {
			break
		}
		token = page.Page.NextPageToken
		pages++
		if pages > 10 {
			t.Fatal("hybrid token chain never terminated")
		}
	}
	if len(walked) != len(fullOrder) {
		t.Fatalf("walked %d hits, full page has %d", len(walked), len(fullOrder))
	}
	for i := range walked {
		if walked[i] != fullOrder[i].ArtifactID {
			t.Fatalf("page walk diverged at %d: %s vs %s", i, walked[i], fullOrder[i].ArtifactID)
		}
	}

	// Foreign scopes stay empty.
	foreign, err := search.SearchHybrid(ctx, indexerapp.SearchInput{OwnerUserID: owner, ProjectID: "01999999-9999-7999-8999-000000000c99", RawQuery: "kubernetes rollout pacing gate", PageSize: 20})
	if err != nil {
		t.Fatalf("foreign hybrid search: %v", err)
	}
	if len(foreign.Page.Hits) != 0 {
		t.Fatalf("foreign project saw %d hybrid hits", len(foreign.Page.Hits))
	}
}

func TestSemanticKnowledgeEqualScorePagination(t *testing.T) {
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
	projection, err := indexerpostgres.New(pool, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := projection.EnsureBootstrapGeneration(ctx, now); err != nil {
		t.Fatal(err)
	}
	owner, project := "01999999-9999-7999-8999-000000000d01", "01999999-9999-7999-8999-000000000d02"
	var expected []string
	for i, age := range []time.Duration{time.Hour, 2 * time.Hour, 2 * time.Hour, 3 * time.Hour} {
		id := fmt.Sprintf("01999999-9999-7999-8999-%012x", 0xd10+i)
		pub := fmt.Sprintf("01999999-9999-7999-8999-%012x", 0xd20+i)
		applySemanticDocument(t, ctx, projection, owner, project, id, pub, "Same score", "search pagination fixture", now.Add(-age), now)
		expected = append(expected, id)
	}
	search := indexerapp.NewSearchServiceForTest(projection)
	for _, size := range []int32{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("page-size-%d", size), func(t *testing.T) {
			query := indexerapp.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "search pagination fixture", PageSize: size}
			var found []string
			var score float64
			for pageNumber := 0; ; pageNumber++ {
				if pageNumber > len(expected) {
					t.Fatal("pagination does not terminate")
				}
				result, err := search.SearchHybrid(ctx, query)
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Page.Hits) == 0 {
					t.Fatal("continuation returned an empty page")
				}
				for _, hit := range result.Page.Hits {
					if len(found) > 0 && hit.Score != score {
						t.Fatal("fixture must have equal scores")
					}
					score = hit.Score
					found = append(found, hit.ArtifactID)
				}
				query.PageToken = result.Page.NextPageToken
				if query.PageToken == "" {
					break
				}
			}
			if strings.Join(found, ",") != strings.Join(expected, ",") {
				t.Fatalf("page order = %v, want %v", found, expected)
			}
		})
	}
}

func applySemanticDocument(t *testing.T, ctx context.Context, projection *indexerpostgres.Repository, owner, project, artifactID, publicationID, title, body string, created, now time.Time) {
	t.Helper()
	if err := projection.ApplyResolvedSource(ctx, indexerports.ResolvedSource{
		Verdict: "resolved", Operation: "review-artifact.upsert",
		OwnerUserID: owner, ProjectID: project,
		ArtifactID: artifactID, ArtifactType: "document.markdown.v1",
		Digest: reviewDigest(body), Title: title, Content: []byte(body),
		CreatedAt: created, PublicationID: publicationID, OccurredAt: now,
	}, indexerdomain.OutcomeApplied, "sha256:"+strings.Repeat("b", 64), now); err != nil {
		t.Fatalf("apply %s: %v", title, err)
	}
}

// TestSemanticKnowledgeStack proves the fused ranking RPC end to end over the
// compose stack: honest capability advertisement, artifact ingest with a
// stored embedding, deterministic hybrid ordering through the Gateway, the
// lexical/hybrid token boundary, and foreign-scope isolation.
func TestSemanticKnowledgeStack(t *testing.T) {
	client := &http.Client{Timeout: 30 * time.Second}
	ctx := context.Background()

	health := commonv1connect.NewSystemServiceClient(client, "http://127.0.0.1:8085")
	reported, healthErr := health.GetServiceHealth(ctx, connect.NewRequest(&commonv1.GetServiceHealthRequest{}))
	if healthErr != nil {
		t.Fatalf("indexer health: %v", healthErr)
	}
	capabilities := map[string]bool{}
	for _, capability := range reported.Msg.GetHealth().GetCapabilities() {
		capabilities[capability.GetId()] = capability.GetAvailable()
	}
	if !capabilities["semantic-hybrid-search"] {
		t.Fatalf("semantic hybrid capability must be available: %+v", capabilities)
	}
	if capabilities["rag"] {
		t.Fatalf("rag must stay honestly unavailable: %+v", capabilities)
	}
	if !capabilities["archive"] {
		t.Fatalf("bounded archive capability must be available: %+v", capabilities)
	}

	projects := projectv1connect.NewProjectServiceClient(client, stackGatewayURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, stackGatewayURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, stackGatewayURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, stackGatewayURL)
	index := indexv1connect.NewIndexServiceClient(client, stackGatewayURL)

	unique := fmt.Sprintf("semquake-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "semantic-" + unique, Name: "Semantic Lab",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	if _, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	})); err != nil {
		t.Fatalf("bind fake provider: %v", err)
	}
	if _, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "semantic-task-" + unique,
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "write the " + unique + " synthetic review document",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	})); err != nil {
		t.Fatalf("submit task: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	var artifact *artifactv1.Artifact
	for time.Now().Before(deadline) {
		listed, listErr := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if listErr == nil && len(listed.Msg.GetArtifacts()) >= 1 {
			got, getErr := artifacts.GetReviewArtifact(ctx, connect.NewRequest(&artifactv1.GetReviewArtifactRequest{
				ArtifactId: listed.Msg.GetArtifacts()[0].GetId(),
			}))
			if getErr == nil {
				artifact = got.Msg.GetArtifact()
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if artifact == nil {
		t.Fatal("review artifact never materialized")
	}

	// Bounded polling until the projection catches up, then the hybrid RPC
	// must surface the artifact with a fused score inside (0,1].
	deadline = time.Now().Add(60 * time.Second)
	var first *indexv1.SearchHybridResponse
	for time.Now().Before(deadline) {
		found, searchErr := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
			ProjectId: project.GetId(),
			Query:     "deterministic synthetic output review fixtures",
			Page:      &commonv1.PageRequest{PageSize: 20},
		}))
		if searchErr == nil && len(found.Msg.GetHits()) >= 1 {
			for _, candidate := range found.Msg.GetHits() {
				if candidate.GetArtifactId() == artifact.GetId() {
					if candidate.GetScore() <= 0 || candidate.GetScore() > 1 {
						t.Fatalf("fused score %v outside (0,1]", candidate.GetScore())
					}
					if strings.TrimSpace(candidate.GetExcerpt()) == "" {
						t.Fatal("hybrid hit must carry a bounded excerpt")
					}
					first = found.Msg
					break
				}
			}
			if first != nil {
				break
			}
		} else if searchErr != nil && !strings.Contains(searchErr.Error(), "Unavailable") {
			t.Fatalf("hybrid search failed: %v", searchErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if first == nil {
		t.Fatal("hybrid search never surfaced the indexed artifact")
	}

	// A second matching artifact converges into a fresh snapshot; from then
	// on no further publications land, so two consecutive hybrid calls must
	// replay the identical deterministic hit sequence.
	if _, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "semantic-followup-" + unique,
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "write the " + unique + " follow-up synthetic review document",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	})); err != nil {
		t.Fatalf("submit follow-up task: %v", err)
	}
	deadline = time.Now().Add(60 * time.Second)
	var converged *indexv1.SearchHybridResponse
	for time.Now().Before(deadline) {
		found, searchErr := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
			ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
			Page: &commonv1.PageRequest{PageSize: 20},
		}))
		if searchErr == nil && len(found.Msg.GetHits()) >= 2 {
			converged = found.Msg
			break
		} else if searchErr != nil && !strings.Contains(searchErr.Error(), "Unavailable") {
			t.Fatalf("hybrid convergence poll failed: %v", searchErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if converged == nil {
		t.Fatal("second artifact never reached the hybrid ranking")
	}
	replay, err := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
		ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
		Page: &commonv1.PageRequest{PageSize: 20},
	}))
	if err != nil {
		t.Fatalf("hybrid replay: %v", err)
	}
	if len(replay.Msg.GetHits()) != len(converged.GetHits()) {
		t.Fatalf("hybrid replay hit count drifted: %d vs %d", len(replay.Msg.GetHits()), len(converged.GetHits()))
	}
	for i := range replay.Msg.GetHits() {
		if replay.Msg.GetHits()[i].GetArtifactId() != converged.GetHits()[i].GetArtifactId() ||
			replay.Msg.GetHits()[i].GetScore() != converged.GetHits()[i].GetScore() {
			t.Fatalf("hybrid replay ordering drifted at %d", i)
		}
	}

	// A lexical page token is invalid on the hybrid RPC and a tampered
	// hybrid token fails closed.
	lexicalPage, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("lexical page one: %v", err)
	}
	if _, err := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
		ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: lexicalPage.Msg.GetPage().GetNextPageToken()},
	})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cross-ranking token must be InvalidArgument, got %v", err)
	}
	hybridPage, err := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
		ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
		Page: &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil {
		t.Fatalf("hybrid page one: %v", err)
	}
	if hybridPage.Msg.GetPage().GetNextPageToken() != "" {
		if _, err := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
			ProjectId: project.GetId(), Query: "deterministic synthetic output review fixtures",
			Page: &commonv1.PageRequest{PageSize: 1, PageToken: hybridPage.Msg.GetPage().GetNextPageToken() + "x"},
		})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("tampered hybrid token must be InvalidArgument, got %v", err)
		}
	}

	// A foreign project with the same query stays empty.
	other, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "semantic-other-" + unique, Name: "Other Semantic Lab",
	}))
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	foreign, err := index.SearchHybrid(ctx, connect.NewRequest(&indexv1.SearchHybridRequest{
		ProjectId: other.Msg.GetProject().GetId(), Query: "deterministic synthetic output review fixtures",
	}))
	if err != nil {
		t.Fatalf("foreign hybrid search: %v", err)
	}
	if len(foreign.Msg.GetHits()) != 0 {
		t.Fatalf("foreign project saw %d hybrid hits", len(foreign.Msg.GetHits()))
	}
}
