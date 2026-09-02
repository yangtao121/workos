//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/dbtx"

	"github.com/jackc/pgx/v5/pgxpool"

	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	indexfeedpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	indexfeedapp "github.com/yangtao121/workos/internal/core/indexfeed/application"
	indexfeeddomain "github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// feedFixture bundles the index publication feed stack over one scratch
// database: the real feed store, the real source authority over the Artifact
// and Project modules, and the real archive path with its tombstone sink.
type feedFixture struct {
	pool           *pgxpool.Pool
	materializer   *orchestration.TaskArtifactMaterializer
	feedService    *indexfeedapp.Service
	projectService *projectapp.Service
	artifactRepo   *artifactpostgres.Repository
	owner          string
	project        string
	task           string
	leaseID        string
	worker         string
}

func newFeedFixture(t *testing.T) *feedFixture {
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

	f := &feedFixture{
		pool: pool, worker: "feed-worker-1",
		owner:   newUUIDForTest(930),
		project: newUUIDForTest(931),
		task:    newUUIDForTest(932),
		leaseID: newUUIDForTest(933),
	}
	execScratch(t, pool, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'Feed Fixture Owner', now()) ON CONFLICT DO NOTHING`, f.owner)
	execScratch(t, pool, `INSERT INTO workos_core.projects (
		id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at
	) VALUES ($1, $2, $3, 'Feed Fixture Project', $4, $5, now(), now())`,
		f.project, f.owner, "feed-seed-"+f.project, newUUIDForTest(934), newUUIDForTest(935))

	feedRepo := indexfeedpostgres.New(pool)
	projectRepo, err := projectpostgres.NewWithFeed(pool, feedRepo)
	if err != nil {
		t.Fatal(err)
	}
	f.projectService = projectapp.New(projectRepo, ids.UUIDv7{})
	agentRepo := agentpostgres.New(pool)
	f.artifactRepo = artifactpostgres.New(pool)
	preparer, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	f.materializer, err = orchestration.NewTaskArtifactMaterializer(pool, agentRepo, f.artifactRepo, preparer, feedRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	artifactService, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := orchestration.NewArtifactProjectScope(f.projectService)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifactService.WithProjectScope(scope); err != nil {
		t.Fatal(err)
	}
	authority, err := orchestration.NewIndexSourceAuthority(artifactService, f.projectService, projectpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	f.feedService, err = indexfeedapp.NewService(feedRepo, authority, pool)
	if err != nil {
		t.Fatal(err)
	}

	payload := `{"targetScope":{"projectId":"` + f.project + `"},"goal":"feed fixture run","outputArtifactTypes":["document.markdown.v1"]}`
	_, err = agentRepo.Create(ctx, agentdomain.Task{
		ID: f.task, OwnerUserID: f.owner, ProjectID: f.project,
		Input: []byte(payload), State: agentdomain.StateQueued, ProviderID: "fake",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, "feed-idempotency-"+f.task)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	lease, err := agentRepo.Claim(ctx, f.worker, 30*time.Second, f.leaseID, time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim task: %v %v", lease, err)
	}
	return f
}

// materialize feeds exactly one markdown document through the real
// materialization stack.
func (f *feedFixture) materialize(t *testing.T, title, body string) string {
	t.Helper()
	artifact, _, err := f.materializer.MaterializeTaskArtifact(context.Background(), f.leaseID, f.worker,
		"document", title, "document.markdown.v1", []byte(body))
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	return artifact.Digest
}

func (f *feedFixture) publicationCount(t *testing.T, operation string) int {
	t.Helper()
	return countScratchRows(t, f.pool,
		`SELECT count(*) FROM workos_core.index_publications WHERE operation = $1`, operation)
}

func (f *feedFixture) archive(t *testing.T) {
	t.Helper()
	if _, err := f.projectService.Archive(context.Background(), f.owner, f.project, 1); err != nil {
		t.Fatalf("archive project: %v", err)
	}
}

func TestIndexFeedPublicationJoinsArtifactTransaction(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()

	digest := f.materialize(t, "Feed Doc", "# Feed Doc\nunique-phrase-feed\n")

	// Exactly one upsert publication, content-free, with the exact digest.
	if got := f.publicationCount(t, indexfeeddomain.OperationReviewArtifactUpsert); got != 1 {
		t.Fatalf("upsert publications = %d, want 1", got)
	}
	var publicationID, storedDigest, storedType string
	var content *[]byte
	if err := f.pool.QueryRow(ctx,
		`SELECT source_id, digest, artifact_type FROM workos_core.index_publications WHERE operation = $1`,
		indexfeeddomain.OperationReviewArtifactUpsert).Scan(&publicationID, &storedDigest, &storedType); err != nil {
		t.Fatal(err)
	}
	if storedDigest != digest || storedType != "document.markdown.v1" {
		t.Fatalf("publication digest/type = %q/%q, want %q/document.markdown.v1", storedDigest, storedType, digest)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT 1 FROM information_schema.columns WHERE table_schema = 'workos_core'
		 AND table_name = 'index_publications' AND column_name = 'content'`).Scan(&content); err == nil {
		t.Fatal("index_publications must not carry a content column")
	}

	// Response-loss replay: the same canonical request replays the first
	// artifact and never mints a second publication.
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker,
		"document", "Feed Doc", "document.markdown.v1", []byte("# Feed Doc\nunique-phrase-feed\n")); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got := f.publicationCount(t, indexfeeddomain.OperationReviewArtifactUpsert); got != 1 {
		t.Fatalf("replay minted a second publication: %d", got)
	}
}

func TestIndexFeedPublicationFailureRollsBackSource(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()

	// A publication sink failure must roll the whole materialization back:
	// zero artifact, zero output mapping, zero event, zero publication.
	agentRepo := agentpostgres.New(f.pool)
	preparer, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	broken, err := orchestration.NewTaskArtifactMaterializer(f.pool, agentRepo, f.artifactRepo, preparer, failSink{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = broken.MaterializeTaskArtifact(ctx, f.leaseID, f.worker,
		"document", "Doomed", "document.markdown.v1", []byte("# Doomed\n"))
	if err == nil {
		t.Fatal("materialization with failing publication sink must fail")
	}
	if got := countScratchRows(t, f.pool, `SELECT count(*) FROM workos_core.project_review_artifacts WHERE source_task_id = $1`, f.task); got != 0 {
		t.Fatalf("artifact rows survived publication failure: %d", got)
	}
	if got := f.publicationCount(t, indexfeeddomain.OperationReviewArtifactUpsert); got != 0 {
		t.Fatalf("publication rows survived their own failure: %d", got)
	}
	if got := countScratchRows(t, f.pool, `SELECT count(*) FROM workos_events.events WHERE stream_id = $1 AND event_type = 'artifact_created'`, f.task); got != 0 {
		t.Fatalf("timeline events survived publication failure: %d", got)
	}
}

// firstReconcileCursor is the decoded "first page" cursor the feed
// application derives from an empty transport token.
const firstReconcileCursor = "v1:0:"

type failSink struct{}

func (failSink) AppendReviewArtifactUpsert(context.Context, dbtx.Tx, indexfeeddomain.Publication) error {
	return errors.New("injected publication failure")
}

func (failSink) AppendProjectTombstone(context.Context, dbtx.Tx, indexfeeddomain.Publication) error {
	return errors.New("injected tombstone failure")
}

func TestIndexFeedClaimResolveCompleteLifecycle(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()
	f.materialize(t, "Lifecycle Doc", "lifecycle-unique-phrase\n")

	claimed, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "feed-worker-a", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d publications, want 1", len(claimed))
	}
	item := claimed[0]
	if item.Publication.Operation != indexfeeddomain.OperationReviewArtifactUpsert || item.LeaseToken == "" {
		t.Fatalf("claimed publication shape: %+v", item)
	}

	verdict, err := f.feedService.Resolve(ctx, "feed-worker-a", item.Publication.ID, item.LeaseToken)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if verdict.Verdict != "resolved" || verdict.Source == nil {
		t.Fatalf("verdict = %q source %v, want resolved", verdict.Verdict, verdict.Source)
	}
	if verdict.Source.Digest != item.Publication.Digest || verdict.Source.Content == nil ||
		!strings.Contains(string(verdict.Source.Content), "lifecycle-unique-phrase") {
		t.Fatalf("resolved source mismatch: %+v", verdict.Source)
	}

	// Wrong lease token / wrong worker are stale, not found.
	if _, err := f.feedService.Resolve(ctx, "feed-worker-a", item.Publication.ID, "wrong-token"); !errors.Is(err, indexfeeddomain.ErrLeaseStale) {
		t.Fatalf("resolve with wrong token = %v, want ErrLeaseStale", err)
	}
	if _, err := f.feedService.Resolve(ctx, "feed-worker-b", item.Publication.ID, item.LeaseToken); !errors.Is(err, indexfeeddomain.ErrLeaseStale) {
		t.Fatalf("resolve with wrong worker = %v, want ErrLeaseStale", err)
	}

	acked, err := f.feedService.Complete(ctx, indexfeedapp.CompleteInput{WorkerID: "feed-worker-a", Results: []indexfeedapp.CompleteEntry{
		{PublicationID: item.Publication.ID, LeaseToken: item.LeaseToken, Outcome: indexfeeddomain.OutcomeCompleted},
	}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !acked[0].Acked {
		t.Fatal("live claim must ack")
	}

	// Completed publications never re-claim.
	again, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "feed-worker-a", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("completed publication was re-claimed: %d", len(again))
	}
}

func TestIndexFeedDoubleClaimantAndLeaseExpiry(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()
	f.materialize(t, "Race Doc", "race-unique-phrase\n")

	first, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "worker-a", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first claim got %d", len(first))
	}

	// A second worker cannot steal a live lease.
	second, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "worker-b", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("live lease was stolen: worker-b claimed %d", len(second))
	}

	// Lease expiry: expire the stored lease the way the database clock
	// would. Worker-b must win the re-claim and worker-a's stale complete
	// must not ack.
	execScratch(t, f.pool,
		`UPDATE workos_core.index_publications SET claim_locked_until = now() - interval '1 second'
		 WHERE outcome IS NULL`)
	stolen, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "worker-b", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(stolen) != 1 {
		t.Fatalf("expired lease was not re-claimable: %d", len(stolen))
	}
	staleAck, err := f.feedService.Complete(ctx, indexfeedapp.CompleteInput{WorkerID: "worker-a", Results: []indexfeedapp.CompleteEntry{
		{PublicationID: first[0].Publication.ID, LeaseToken: first[0].LeaseToken, Outcome: indexfeeddomain.OutcomeCompleted},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if staleAck[0].Acked {
		t.Fatal("stale complete must not ack")
	}
	freshAck, err := f.feedService.Complete(ctx, indexfeedapp.CompleteInput{WorkerID: "worker-b", Results: []indexfeedapp.CompleteEntry{
		{PublicationID: stolen[0].Publication.ID, LeaseToken: stolen[0].LeaseToken, Outcome: indexfeeddomain.OutcomeCompleted},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !freshAck[0].Acked {
		t.Fatal("live complete must ack")
	}
}

func TestIndexFeedArchiveTombstoneAndConcurrentUpsert(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()
	f.materialize(t, "Archive Race Doc", "archive-race-unique-phrase\n")
	f.archive(t)

	// Exactly one tombstone publication, from the same archive transaction.
	if got := f.publicationCount(t, indexfeeddomain.OperationProjectTombstone); got != 1 {
		t.Fatalf("tombstone publications = %d, want 1", got)
	}
	// Archive replay with a stale revision must not mint a second tombstone.
	if _, err := f.projectService.Archive(ctx, f.owner, f.project, 1); err == nil {
		t.Fatal("stale archive revision must fail")
	}
	if got := f.publicationCount(t, indexfeeddomain.OperationProjectTombstone); got != 1 {
		t.Fatalf("stale archive minted a second tombstone: %d", got)
	}

	claimed, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "worker-a", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d publications, want upsert + tombstone", len(claimed))
	}
	// The archived-project upsert and the tombstone publication must both
	// resolve to the authoritative tombstoned verdict, never to searchable
	// content.
	verdicts := map[string]string{}
	for _, item := range claimed {
		verdict, err := f.feedService.Resolve(ctx, "worker-a", item.Publication.ID, item.LeaseToken)
		if err != nil {
			t.Fatalf("resolve %s: %v", item.Publication.Operation, err)
		}
		if verdict.Source != nil {
			t.Fatalf("verdict for %s must not carry content", item.Publication.Operation)
		}
		verdicts[item.Publication.Operation] = verdict.Verdict
	}
	if verdicts[indexfeeddomain.OperationReviewArtifactUpsert] != "tombstoned" {
		t.Fatalf("archived-project upsert verdict = %q, want tombstoned", verdicts[indexfeeddomain.OperationReviewArtifactUpsert])
	}
	if verdicts[indexfeeddomain.OperationProjectTombstone] != "tombstoned" {
		t.Fatalf("tombstone verdict = %q, want tombstoned", verdicts[indexfeeddomain.OperationProjectTombstone])
	}
}

func TestIndexFeedReconciliationPages(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()
	digest := f.materialize(t, "Reconcile Doc", "reconcile-unique-phrase\n")

	artifactService, err := artifactapp.New(f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := orchestration.NewArtifactProjectScope(f.projectService)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifactService.WithProjectScope(scope); err != nil {
		t.Fatal(err)
	}
	authority, err := orchestration.NewIndexSourceAuthority(artifactService, f.projectService, projectpostgres.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	sources, next, err := authority.ReconcileSources(ctx, 100, firstReconcileCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || next != "" {
		t.Fatalf("active sources = %d next %q, want 1 and empty", len(sources), next)
	}
	if sources[0].Digest != digest || sources[0].ArtifactID == "" {
		t.Fatalf("source summary mismatch: %+v", sources[0])
	}

	verified, err := authority.ResolveSourceContent(ctx, f.owner, f.project, sources[0].ArtifactID, digest)
	if err != nil {
		t.Fatalf("resolve source content: %v", err)
	}
	if !strings.Contains(string(verified.Content), "reconcile-unique-phrase") {
		t.Fatalf("resolved content mismatch: %q", verified.Content)
	}
	// Digest pinning: a stale digest never returns replaced bytes.
	if _, err := authority.ResolveSourceContent(ctx, f.owner, f.project, sources[0].ArtifactID, "sha256:"+strings.Repeat("ab", 32)); err == nil {
		t.Fatal("digest-pinned content resolve must fail on mismatch")
	}

	// After archive: the project leaves the active page and appears in the
	// archived page.
	f.archive(t)
	sources, _, err = authority.ReconcileSources(ctx, 100, firstReconcileCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("archived project still in active source page: %d", len(sources))
	}
	archived, _, err := authority.ReconcileArchivedProjects(ctx, 100, firstReconcileCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 1 || archived[0].ProjectID != f.project {
		t.Fatalf("archived page = %+v, want the fixture project", archived)
	}
}

func TestIndexFeedStoredContentCorruptionIsTerminalVerdict(t *testing.T) {
	t.Parallel()
	f := newFeedFixture(t)
	ctx := context.Background()
	f.materialize(t, "Corrupt Doc", "corrupt-unique-phrase\n")

	// Corrupt the stored bytes behind Core's back: the resolve must fail
	// closed as terminal corruption, never silently hand out new bytes.
	execScratch(t, f.pool,
		`UPDATE workos_core.project_review_artifacts
		 SET content = content || $1, byte_count = byte_count + $2 WHERE source_task_id = $3`,
		[]byte("\ntampered\n"), len("\ntampered\n"), f.task)

	claimed, err := f.feedService.Claim(ctx, indexfeedapp.ClaimInput{WorkerID: "worker-a", MaxBatch: 8, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d", len(claimed))
	}
	verdict, err := f.feedService.Resolve(ctx, "worker-a", claimed[0].Publication.ID, claimed[0].LeaseToken)
	if err != nil {
		t.Fatalf("resolve corruption: %v", err)
	}
	if verdict.Verdict != "corrupt" {
		t.Fatalf("corruption verdict = %q, want corrupt", verdict.Verdict)
	}
}
