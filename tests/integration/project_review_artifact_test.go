//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"

	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	indexfeedpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// 021 is checksum-pinned like every shipped migration: it is already applied
// on the acceptance volume, so any later edit is a hard failure.
const reviewArtifacts021 = "80cb4466c5d8ddb35513b8f87dbc79963269e53fb11ec01d3daa3ba77850b5fe"

// 022 is forward-only because 021 may already be applied on an acceptance
// volume. Its checksum is pinned after the integrity constraints are final.
const reviewArtifacts022 = "5f7c4a88525d9d47123246a67b5b8dc2b3d59355d892652c0bc59b38976244e6"

// reviewFixture bundles the materialization stack over one scratch database.
type reviewFixture struct {
	dsn          string
	pool         *pgxpool.Pool
	materializer *orchestration.TaskArtifactMaterializer
	agentRepo    *agentpostgres.Repository
	artifactRepo *artifactpostgres.Repository
	owner        string
	project      string
	task         string
	leaseID      string
	worker       string
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	f := &reviewFixture{
		dsn: dsn, pool: pool, worker: "review-worker-1",
		owner:   newUUIDForTest(910),
		project: newUUIDForTest(911),
		task:    newUUIDForTest(912),
		leaseID: newUUIDForTest(913),
	}
	// Seed owner, project (agent_tasks.project_id FK), and one queued task
	// with the canonical requested artifact types, then claim it the way the
	// harness worker does.
	execScratch(t, pool, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'Review Fixture Owner', now()) ON CONFLICT DO NOTHING`, f.owner)
	execScratch(t, pool, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at
	) VALUES ($1, $2, $3, 'Review Fixture Project', $4, $5, now(), now())`,
		f.project, f.owner, "review-seed-"+f.project, newUUIDForTest(914), newUUIDForTest(915))

	agentRepo := agentpostgres.New(pool)
	artifactRepo := artifactpostgres.New(pool)
	f.agentRepo, f.artifactRepo = agentRepo, artifactRepo
	payload := fmt.Sprintf(
		`{"targetScope":{"projectId":%q},"goal":"synthetic review run","outputArtifactTypes":["document.markdown.v1","code.unified-diff.v1"]}`,
		f.project,
	)
	_, err = agentRepo.Create(ctx, agentdomain.Task{
		ID: f.task, OwnerUserID: f.owner, ProjectID: f.project,
		Input: []byte(payload), State: agentdomain.StateQueued, ProviderID: "fake",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, "review-idempotency-"+f.task)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	lease, err := agentRepo.Claim(ctx, f.worker, 30*time.Second, f.leaseID, time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim task: %v %v", lease, err)
	}

	preparer, err := artifactapp.New(artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := orchestration.NewTaskArtifactMaterializer(pool, agentRepo, artifactRepo, preparer, indexfeedpostgres.New(pool), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	f.materializer = materializer
	return f
}

func execScratch(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), statement, args...); err != nil {
		t.Fatalf("exec %q: %v", firstLine(statement), err)
	}
}

func (f *reviewFixture) eventCount(t *testing.T, eventType string) int {
	t.Helper()
	return countScratchRows(t, f.pool,
		`SELECT count(*) FROM workos_events.events WHERE stream_id = $1 AND event_type = $2`, f.task, eventType)
}

func (f *reviewFixture) artifactCount(t *testing.T) int {
	t.Helper()
	return countScratchRows(t, f.pool,
		`SELECT count(*) FROM workos_core.project_review_artifacts WHERE source_task_id = $1`, f.task)
}

func (f *reviewFixture) outputCount(t *testing.T) int {
	t.Helper()
	return countScratchRows(t, f.pool,
		`SELECT count(*) FROM workos_core.project_review_artifact_outputs WHERE task_id = $1`, f.task)
}

func TestProjectReviewArtifactMigrationConstraints(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()
	for name, want := range map[string]string{
		"021_project_review_artifacts.sql":          reviewArtifacts021,
		"022_project_review_artifact_integrity.sql": reviewArtifacts022,
	} {
		var got string
		if err := f.pool.QueryRow(ctx,
			`SELECT checksum FROM workos_meta.schema_migrations WHERE name = $1`, name,
		).Scan(&got); err != nil || got != want {
			t.Fatalf("migration %s checksum = %q (%v), want %q", name, got, err, want)
		}
	}
	for _, probe := range []struct {
		name      string
		artifact  string
		media     string
		outputKey string
		byteLen   int
	}{
		{"type/media mismatch", "document.markdown.v1", "text/x-diff; charset=utf-8", "k1", 8},
		{"unknown type", "chart.v1", "text/markdown; charset=utf-8", "k2", 8},
		{"invalid output key", "document.markdown.v1", "text/markdown; charset=utf-8", "UPPER", 8},
		{"over byte limit", "document.markdown.v1", "text/markdown; charset=utf-8", "k3", 524289},
	} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO workos_core.project_review_artifacts (
			id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
			output_key, byte_count, line_count, content, created_at
		) VALUES (
			$1, $2, $3, 't', $4, 'sha256:' || repeat('a', 64), $5, $6, $7, $8, 1, repeat('x', $8), now()
		)`, newUUIDForTest(950), f.owner, probe.artifact, probe.media, f.project, newUUIDForTest(951),
			probe.outputKey, probe.byteLen); err == nil {
			t.Fatalf("%s: constraint not enforced", probe.name)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO workos_core.project_review_artifacts (
		id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
		output_key, byte_count, line_count, content, created_at
	) VALUES ($1, $2, 'document.markdown.v1', 't', 'text/markdown; charset=utf-8',
		'sha256:' || repeat('a', 64), $3, $4, 'size-drift', 2, 1, decode('78', 'hex'), now())`,
		newUUIDForTest(952), f.owner, f.project, newUUIDForTest(953)); err == nil {
		t.Fatal("byte_count/content drift constraint not enforced")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO workos_core.project_review_artifacts (
		id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
		output_key, byte_count, line_count, content, created_at
	) VALUES ($1, $2, 'document.markdown.v1', 't', 'text/markdown; charset=utf-8',
		'sha256:' || repeat('a', 64), $3, $4, 'infinite-time', 1, 1, decode('78', 'hex'), 'infinity')`,
		newUUIDForTest(954), f.owner, f.project, newUUIDForTest(955)); err == nil {
		t.Fatal("infinite artifact timestamp constraint not enforced")
	}
}

func TestReviewArtifactMaterializationReplayAndConflict(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()

	artifact, event, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"  Fake Harness Review Document  ", "document.markdown.v1", []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.GetProjectId() != f.project || artifact.GetSourceTaskId() != f.task ||
		artifact.GetTitle() != "Fake Harness Review Document" {
		t.Fatalf("unexpected artifact projection: %#v", artifact)
	}
	if event.GetSequence() != 1 || event.GetArtifactCreated().GetArtifactId() != artifact.GetId() {
		t.Fatalf("unexpected Core-minted event: %#v", event)
	}
	if f.eventCount(t, "artifact_created") != 1 || f.artifactCount(t) != 1 || f.outputCount(t) != 1 {
		t.Fatalf("unexpected fact counts: events=%d artifacts=%d outputs=%d",
			f.eventCount(t, "artifact_created"), f.artifactCount(t), f.outputCount(t))
	}

	// Replay with the same canonical request (different title whitespace
	// normalizes identically) returns the first artifact and event without
	// re-publishing.
	replayed, replayEvent, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Fake Harness Review Document", "document.markdown.v1", []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GetId() != artifact.GetId() || replayEvent.GetId() != event.GetId() ||
		replayEvent.GetSequence() != event.GetSequence() {
		t.Fatalf("replay diverged: %#v/%#v", replayed, replayEvent)
	}
	if f.eventCount(t, "artifact_created") != 1 || f.artifactCount(t) != 1 {
		t.Fatal("replay published facts again")
	}

	// Same key, different content: stable conflict.
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Fake Harness Review Document", "document.markdown.v1", []byte("# Different\n")); !errors.Is(err, artifactdomain.ErrOutputConflict) {
		t.Fatalf("expected stable conflict, got %v", err)
	}
	// Same type, different key: the (task, type) slot conflicts too.
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document-2",
		"Fake Harness Review Document", "document.markdown.v1", []byte("# Hello\n")); !errors.Is(err, artifactdomain.ErrOutputConflict) {
		t.Fatalf("expected type-slot conflict, got %v", err)
	}
	if f.artifactCount(t) != 1 || f.outputCount(t) != 1 {
		t.Fatal("conflicts left orphan facts")
	}

	// The second requested type materializes independently at the next
	// sequence.
	second, secondEvent, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "patch",
		"Fake Harness Proposed Patch", "code.unified-diff.v1", []byte("diff --git a/src/example.ts b/src/example.ts\n"))
	if err != nil {
		t.Fatal(err)
	}
	if secondEvent.GetSequence() != 2 || second.GetType() != "code.unified-diff.v1" {
		t.Fatalf("unexpected second publication: %#v/%#v", second, secondEvent)
	}
}

func TestReviewArtifactRejectsLostLeaseAndForeignReads(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, "wrong-worker", "document",
		"Title", "document.markdown.v1", []byte("# Hello\n")); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("wrong worker must lose the lease, got %v", err)
	}
	artifact, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Title", "document.markdown.v1", []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}

	application, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	projectScope, err := orchestration.NewArtifactProjectScope(
		projectapp.New(projectpostgres.New(f.pool), ids.UUIDv7{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.WithProjectScope(projectScope); err != nil {
		t.Fatal(err)
	}
	if _, _, err := application.GetReview(ctx, "01999999-9999-7999-8999-000000000099", artifact.GetId()); !errors.Is(err, artifactdomain.ErrNotFound) {
		t.Fatalf("foreign owner must be NotFound, got %v", err)
	}
	fact, content, err := application.GetReview(ctx, f.owner, artifact.GetId())
	if err != nil || fact.ID != artifact.GetId() || string(content.Content) != "# Hello\n" {
		t.Fatalf("owner read failed: %v %s", err, content.Content)
	}

	// Stored corruption is Internal, never NotFound or InvalidArgument.
	execScratch(t, f.pool, `UPDATE workos_core.project_review_artifacts SET content = $2 WHERE id = $1`,
		artifact.GetId(), []byte("# Jello\n"))
	if _, _, err := application.GetReview(ctx, f.owner, artifact.GetId()); err == nil ||
		errors.Is(err, artifactdomain.ErrNotFound) || errors.Is(err, artifactdomain.ErrInvalid) {
		t.Fatalf("stored corruption must surface as internal, got %v", err)
	}
	if _, err := application.Get(ctx, f.owner, artifact.GetId()); !errors.Is(err, artifactdomain.ErrCorrupt) {
		t.Fatalf("metadata Get must also revalidate stored content, got %v", err)
	}
}

func TestReviewArtifactReplayVerifiesDurableAgentPublication(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()
	artifact, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Review", "document.markdown.v1", []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workos_core.project_review_artifact_outputs
		SET project_id = $2 WHERE task_id = $1 AND output_key = 'document'`,
		f.task, newUUIDForTest(957)); err == nil {
		t.Fatal("output mapping was allowed to drift from its Artifact-owned binding")
	}
	// The Artifact mapping deliberately has no cross-module FK into the Agent
	// event table. If its reference drifts, replay must verify the Agent-owned
	// row rather than reconstructing and returning a fabricated event.
	execScratch(t, f.pool, `UPDATE workos_core.project_review_artifact_outputs
		SET event_id = $2 WHERE task_id = $1 AND output_key = 'document'`,
		f.task, newUUIDForTest(956))
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Review", "document.markdown.v1", []byte("# Hello\n")); !errors.Is(err, artifactdomain.ErrCorrupt) {
		t.Fatalf("missing durable publication was replayed: artifact=%s err=%v", artifact.GetId(), err)
	}
	if f.eventCount(t, "artifact_created") != 1 || f.artifactCount(t) != 1 {
		t.Fatal("corrupt replay wrote a second artifact or event")
	}
}

func TestReviewArtifactConcurrentMaterialization(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	// A second lease on the same task models worker reclaim: cancel-free
	// claim of the same task is serialized by the outbox row, so instead run
	// two materializations through the SAME lease concurrently — the exact
	// provider-retry race the adjudication exists for.
	const goroutines = 8
	type outcome struct {
		artifactID string
		err        error
	}
	outcomes := make(chan outcome, goroutines)
	var start sync.WaitGroup
	start.Add(1)
	for index := 0; index < goroutines; index++ {
		go func(index int) {
			start.Wait()
			artifact, _, err := f.materializer.MaterializeTaskArtifact(context.Background(), f.leaseID, f.worker,
				"document", "Concurrent Title", "document.markdown.v1", []byte(fmt.Sprintf("# Version %d\n", index%2)))
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			outcomes <- outcome{artifactID: artifact.GetId()}
		}(index)
	}
	start.Done()
	winners := map[string]int{}
	conflicts := 0
	for index := 0; index < goroutines; index++ {
		result := <-outcomes
		switch {
		case result.err == nil:
			winners[result.artifactID]++
		case errors.Is(result.err, artifactdomain.ErrOutputConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent outcome: %v", result.err)
		}
	}
	// Two canonical requests race for one (task, output key): whichever
	// commits first wins the mapping; its same-digest peers replay the same
	// artifact, every different-digest call conflicts, and no orphan rows
	// remain.
	if len(winners) != 1 || conflicts != goroutines/2 {
		t.Fatalf("unexpected adjudication: winners=%v conflicts=%d", winners, conflicts)
	}
	for artifactID, count := range winners {
		if count != goroutines/2 {
			t.Fatalf("winner %s was returned %d times, expected %d replays", artifactID, count, goroutines/2)
		}
	}
	if f.artifactCount(t) != 1 || f.outputCount(t) != 1 || f.eventCount(t, "artifact_created") != 1 {
		t.Fatalf("concurrent race left inconsistent facts: artifacts=%d outputs=%d events=%d",
			f.artifactCount(t), f.outputCount(t), f.eventCount(t, "artifact_created"))
	}
}

func TestReviewArtifactListPagingAndRestart(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()
	// The first task requests both canonical types: one markdown plus one
	// unified diff, the maximum a single task can materialize.
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Document 0", "document.markdown.v1", []byte("# Doc 0\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "patch",
		"Patch 1", "code.unified-diff.v1", []byte("diff --git a/src/example.ts b/src/example.ts\n")); err != nil {
		t.Fatal(err)
	}
	// A (task, type) pair is unique, so further pages come from separate
	// tasks sharing the project.
	for index := 2; index < 5; index++ {
		taskID := newUUIDForTest(960 + index)
		leaseID := newUUIDForTest(970 + index)
		payload := fmt.Sprintf(
			`{"targetScope":{"projectId":%q},"goal":"seed","outputArtifactTypes":["document.markdown.v1"]}`, f.project)
		if _, err := f.agentRepo.Create(ctx, agentdomain.Task{
			ID: taskID, OwnerUserID: f.owner, ProjectID: f.project,
			Input: []byte(payload), State: agentdomain.StateQueued, ProviderID: "fake",
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}, "review-idempotency-"+taskID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.agentRepo.Claim(ctx, f.worker, 30*time.Second, leaseID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, leaseID, f.worker, "document",
			fmt.Sprintf("Document %d", index), "document.markdown.v1", []byte(fmt.Sprintf("# Doc %d\n", index))); err != nil {
			t.Fatal(err)
		}
	}

	application, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	// Project listing requires the neutral project scope port, exactly like
	// the composition root wires it.
	projectScope, err := orchestration.NewArtifactProjectScope(
		projectapp.New(projectpostgres.New(f.pool), ids.UUIDv7{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.WithProjectScope(projectScope); err != nil {
		t.Fatal(err)
	}
	pageSize := 2
	seen := map[string]int{}
	cursor := ""
	for {
		result, err := application.List(ctx, f.owner, f.project, cursor, pageSize)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.Items {
			seen[item.ID]++
		}
		if result.NextToken == "" {
			break
		}
		if len(result.Items) != pageSize {
			t.Fatalf("short page with a next token: %d", len(result.Items))
		}
		cursor = result.NextToken
	}
	if len(seen) != 5 {
		t.Fatalf("paging lost or duplicated rows: %d unique", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("artifact %s appeared %d times", id, count)
		}
	}

	// "Restart": fresh repositories over the same database replay and read
	// identically.
	restartedPool, err := pgxpool.New(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedPool.Close()
	restartedAgent := agentpostgres.New(restartedPool)
	restartedArtifacts := artifactpostgres.New(restartedPool)
	restartedPreparer, err := artifactapp.New(restartedArtifacts, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := orchestration.NewTaskArtifactMaterializer(restartedPool, restartedAgent, restartedArtifacts, restartedPreparer, indexfeedpostgres.New(restartedPool), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayEvent, err := restarted.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Document 0", "document.markdown.v1", []byte("# Doc 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly five publications exist across the four tasks (2 from the
	// first), and the replay still returns the first event of its stream.
	if replayEvent.GetSequence() != 1 || f.eventCount(t, "artifact_created") != 2 {
		t.Fatalf("restart replay re-published: sequence=%d events=%d", replayEvent.GetSequence(), f.eventCount(t, "artifact_created"))
	}
	if total := countScratchRows(t, f.pool,
		`SELECT count(*) FROM workos_events.events WHERE event_type = 'artifact_created'`); total != 5 {
		t.Fatalf("unexpected total publications: %d", total)
	}
	if strings.TrimSpace(replayed.GetTitle()) != "Document 0" {
		t.Fatalf("unexpected replayed artifact: %#v", replayed)
	}
}

func TestReviewArtifactOutputDigestCoversTitleAndContent(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	ctx := context.Background()
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
		"Title A", "document.markdown.v1", []byte("# Hello\n")); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		name    string
		title   string
		content string
	}{
		{"title drift", "Title B", "# Hello\n"},
		{"content drift", "Title A", "# Hello \n"},
		{"type drift", "Title A", "diff --git a/src/example.ts b/src/example.ts\n"},
	} {
		artifactType := "document.markdown.v1"
		if strings.HasPrefix(probe.content, "diff --git") {
			artifactType = "code.unified-diff.v1"
		}
		if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document",
			probe.title, artifactType, []byte(probe.content)); !errors.Is(err, artifactdomain.ErrOutputConflict) {
			t.Fatalf("%s must conflict, got %v", probe.name, err)
		}
	}
	_ = uuid.New()
}
