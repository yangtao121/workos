//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	indexerpostgres "github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerports "github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// The disaster-recovery rebuild gate (ADR-0013 §F3): Core-authoritative
// shadow-generation rebuild over a real PostgreSQL projection. The test owns
// a scratch database and stands in for Core's authority pages with a fake
// feed reading the same seeded rows — the composition under test (executor,
// rebuild store, generation CAS, receipts, search) is the real one. The
// destruction/restore below targets ONLY this temporary indexer-owned
// database; the compose stack's volumes are never touched.

type rebuildFixture struct {
	pool  *pgxpool.Pool
	dsn   string
	proj  *indexerpostgres.Repository
	ids   ids.Generator
	owner string
	// foreignOwner owns nothing; searches under it must always be empty.
	foreignOwner string
}

func newRebuildFixture(t *testing.T) *rebuildFixture {
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
	generator := ids.UUIDv7{}
	projection, err := indexerpostgres.New(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	f := &rebuildFixture{
		pool: pool, dsn: dsn, proj: projection, ids: generator,
		owner:        "01999999-9999-7999-8999-000000000940",
		foreignOwner: "01999999-9999-7999-8999-000000000941",
	}
	execScratch(t, pool, `INSERT INTO workos_core.users (id, kind, display_name, created_at)
		VALUES ($1, 'owner', 'Rebuild Fixture', now()) ON CONFLICT DO NOTHING`, f.owner)

	// Seed the deployment's single owner (users_single_owner_idx), three
	// active projects with review artifacts, and one archived project.
	// Foreign-owner isolation is exercised through the search path with an
	// owner id that owns nothing.
	for _, seed := range []struct{ id, owner, name string }{
		{"01999999-9999-7999-8999-000000000942", f.owner, "Rebuild Alpha"},
		{"01999999-9999-7999-8999-000000000943", f.owner, "Rebuild Beta"},
		{"01999999-9999-7999-8999-000000000944", f.owner, "Rebuild Gamma"},
		{"01999999-9999-7999-8999-000000000945", f.owner, "Rebuild Archived"},
	} {
		execScratch(t, pool, `INSERT INTO workos_core.projects (
			id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id,
			created_at, updated_at, archived_at
		) VALUES ($1, $2, $3, $4, $5, $6, now(), now(),
			CASE WHEN $4 = 'Rebuild Archived' THEN now() ELSE NULL END)`,
			seed.id, seed.owner, "rebuild-seed-"+seed.id, seed.name,
			newUUIDForTest(946), newUUIDForTest(947))
	}
	for _, seed := range []struct{ id, project, title, body string }{
		{"01999999-9999-7999-8999-000000000950", "01999999-9999-7999-8999-000000000942", "Alpha Document", "# Alpha Document\nrebuild-unique-alpha-phrase\n"},
		{"01999999-9999-7999-8999-000000000951", "01999999-9999-7999-8999-000000000943", "Beta Patch", "# Beta Patch\nrebuild-unique-beta-phrase\n"},
		{"01999999-9999-7999-8999-000000000952", "01999999-9999-7999-8999-000000000944", "Gamma Document", "# Gamma\nrebuild-unique-gamma-phrase\n"},
		{"01999999-9999-7999-8999-000000000953", "01999999-9999-7999-8999-000000000945", "Archived Document", "# Archived\nrebuild-unique-archived-phrase\n"},
	} {
		execScratch(t, pool, `INSERT INTO workos_core.project_review_artifacts (
			id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
			output_key, byte_count, line_count, content, created_at
		) VALUES (
			$1, $2, 'document.markdown.v1', $3, 'text/markdown; charset=utf-8', $4, $5, $6,
			'document', $7, $8, $9, now()
		)`,
			seed.id, f.owner, seed.title, reviewDigest(seed.body), seed.project,
			"01999999-9999-7999-8999-000000000960",
			len(seed.body), strings.Count(seed.body, "\n"), seed.body)
	}
	return f
}

// reviewDigest mirrors the artifact domain's versioned digest for fixtures.
func reviewDigest(body string) string {
	h := sha256.New()
	h.Write([]byte("workos.review-artifact.v1"))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// fakeRebuildFeed reads the seeded Core-owned rows of the scratch database —
// the same pages the real IndexPublicationSourceService would produce.
type fakeRebuildFeed struct {
	pool *pgxpool.Pool
	proj *indexerpostgres.Repository
}

func (f *fakeRebuildFeed) ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]indexerports.ReconcileSource, string, string, error) {
	first := "v1:0:"
	if cursor == "" {
		cursor = first
	}
	rows, err := f.pool.Query(ctx, `
		SELECT a.owner_user_id::text, a.project_id::text, a.id::text, a.type, a.digest, a.created_at
		FROM workos_core.project_review_artifacts a
		JOIN workos_core.projects p ON p.id = a.project_id AND p.archived_at IS NULL
		WHERE (a.created_at, a.id) > ($1::timestamptz, $2::uuid)
		ORDER BY a.created_at, a.id LIMIT $3`,
		decodeTestCursorTime(cursor), decodeTestCursorID(cursor), pageSize)
	if err != nil {
		return nil, "", "", err
	}
	defer rows.Close()
	facts := make([]indexerports.ReconcileSource, 0, pageSize)
	var lastTime time.Time
	var lastID string
	for rows.Next() {
		var fact indexerports.ReconcileSource
		if err := rows.Scan(&fact.OwnerUserID, &fact.ProjectID, &fact.ArtifactID, &fact.ArtifactType, &fact.Digest, &fact.CreatedAt); err != nil {
			return nil, "", "", err
		}
		facts = append(facts, fact)
		lastTime, lastID = fact.CreatedAt, fact.ArtifactID
	}
	if len(facts) < pageSize {
		return facts, "", encodeTestCursor(lastTime, lastID), nil
	}
	return facts, encodeTestCursor(lastTime, lastID), encodeTestCursor(lastTime, lastID), nil
}

func decodeTestCursorTime(cursor string) time.Time {
	parts := strings.Split(cursor, ":")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}
	}
	var micros int64
	fmt.Sscanf(parts[1], "%d", &micros)
	return time.UnixMicro(micros)
}

func decodeTestCursorID(cursor string) string {
	parts := strings.Split(cursor, ":")
	if len(parts) != 3 {
		return "00000000-0000-0000-0000-000000000000"
	}
	if parts[2] == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return parts[2]
}

func encodeTestCursor(at time.Time, id string) string {
	return "v1:" + fmt.Sprintf("%d", at.UnixMicro()) + ":" + id
}

func (f *fakeRebuildFeed) CountPending(ctx context.Context) (int64, error) { return 0, nil }

func (f *fakeRebuildFeed) ResolveSnapshotSource(ctx context.Context, ownerUserID, projectID, artifactID, digest string) (string, string, string, []byte, bool, error) {
	var title, storedDigest, taskID, artifactType string
	var content []byte
	var archived bool
	err := f.pool.QueryRow(ctx, `
		SELECT a.title, a.source_task_id::text, a.content, a.digest, a.type, p.archived_at IS NOT NULL
		FROM workos_core.project_review_artifacts a
		JOIN workos_core.projects p ON p.id = a.project_id
		WHERE a.owner_user_id = $1::uuid AND a.project_id = $2::uuid AND a.id = $3::uuid`,
		ownerUserID, projectID, artifactID).Scan(&title, &taskID, &content, &storedDigest, &artifactType, &archived)
	if err != nil {
		return "", "", "", nil, false, fmt.Errorf("resolve snapshot source: %w", err)
	}
	if archived || storedDigest != digest {
		return "", "", "", nil, false, nil
	}
	return title, taskID, artifactType, content, true, nil
}

func (f *fakeRebuildFeed) ActiveGenerationID(ctx context.Context) (string, error) {
	return f.proj.ActiveGenerationID(ctx)
}

// buildExecutor constructs the real executor over the fixture's database.
// Every call returns a FRESH executor instance over the same durable state:
// exactly what a process restart produces.
func (f *rebuildFixture) buildExecutor(t *testing.T) (*indexerapp.RebuildExecutor, *fakeRebuildFeed) {
	return f.buildExecutorOn(t, f.pool, f.proj)
}

func (f *rebuildFixture) buildExecutorOn(t *testing.T, pool *pgxpool.Pool, projection *indexerpostgres.Repository) (*indexerapp.RebuildExecutor, *fakeRebuildFeed) {
	t.Helper()
	feed := &fakeRebuildFeed{pool: pool, proj: projection}
	executor, err := indexerapp.NewRebuildExecutor(
		mustRebuildStore(t, pool, f.ids),
		indexerapp.NewRebuildDriver(&portsCoreFeedAdapter{fake: feed, proj: projection}, f.ids), 100)
	if err != nil {
		t.Fatal(err)
	}
	return executor, feed
}

// portsCoreFeedAdapter exposes the fake feed through the indexer ports.CoreFeed
// surface the reconciliation pass consumes; claim/complete paths are unused
// in this fixture and fail loudly if ever called.
type portsCoreFeedAdapter struct {
	fake *fakeRebuildFeed
	proj *indexerpostgres.Repository
}

func (a *portsCoreFeedAdapter) Claim(context.Context, string, int, time.Duration) ([]indexerports.ClaimedPublication, error) {
	return nil, errors.New("claim is not part of the rebuild fixture")
}

func (a *portsCoreFeedAdapter) Resolve(context.Context, string, string, string) (indexerports.ResolvedSource, error) {
	return indexerports.ResolvedSource{}, errors.New("resolve is not part of the rebuild fixture")
}

func (a *portsCoreFeedAdapter) Complete(context.Context, string, []indexerports.ConsumptionResult) ([]bool, error) {
	return nil, errors.New("complete is not part of the rebuild fixture")
}

func (a *portsCoreFeedAdapter) CountPending(ctx context.Context) (int64, error) {
	return a.fake.CountPending(ctx)
}

func (a *portsCoreFeedAdapter) ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]indexerports.ReconcileSource, string, string, error) {
	facts, next, watermark, err := a.fake.ReconcileSources(ctx, pageSize, cursor)
	if err != nil {
		return nil, "", "", err
	}
	out := make([]indexerports.ReconcileSource, 0, len(facts))
	for _, fact := range facts {
		out = append(out, indexerports.ReconcileSource{
			OwnerUserID: fact.OwnerUserID, ProjectID: fact.ProjectID,
			ArtifactID: fact.ArtifactID, ArtifactType: fact.ArtifactType,
			Digest: fact.Digest, CreatedAt: fact.CreatedAt,
		})
	}
	return out, next, watermark, nil
}

func (a *portsCoreFeedAdapter) ReconcileArchivedProjects(context.Context, int, string) ([]indexerports.ArchivedProject, string, error) {
	return nil, "", nil
}

func (a *portsCoreFeedAdapter) ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (indexerports.ResolvedSource, error) {
	title, _, artifactType, content, resolvable, err := a.fake.ResolveSnapshotSource(ctx, ownerUserID, projectID, artifactID, expectedDigest)
	if err != nil {
		return indexerports.ResolvedSource{}, err
	}
	if !resolvable {
		return indexerports.ResolvedSource{}, indexerports.ErrNotFound
	}
	return indexerports.ResolvedSource{
		Verdict: "resolved", Operation: "review-artifact.upsert",
		OwnerUserID: ownerUserID, ProjectID: projectID,
		ArtifactID: artifactID, ArtifactType: artifactType, Digest: expectedDigest,
		Title: title, Content: content,
	}, nil
}

func (a *portsCoreFeedAdapter) ActiveGenerationID(ctx context.Context) (string, error) {
	return a.proj.ActiveGenerationID(ctx)
}

func mustRebuildStore(t *testing.T, pool *pgxpool.Pool, generator ids.Generator) indexerapp.RebuildStore {
	t.Helper()
	store, err := indexerpostgres.NewRebuildStore(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// buildSearchProjection exposes the owner-facing search for golden reads.
func (f *rebuildFixture) buildSearch(t *testing.T) *indexerapp.SearchService {
	t.Helper()
	return indexerapp.NewSearchServiceForTest(f.proj)
}

// rebuildGolden is the deterministic search golden for one project.
type rebuildGolden struct {
	ProjectID string
	Hits      []string // "artifactID:digest:title"
}

func (f *rebuildFixture) captureGolden(t *testing.T, search *indexerapp.SearchService, projectID, owner, phrase string) rebuildGolden {
	t.Helper()
	result, err := search.Search(context.Background(), indexerapp.SearchInput{
		OwnerUserID: owner, ProjectID: projectID, RawQuery: phrase, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("golden search: %v", err)
	}
	golden := rebuildGolden{ProjectID: projectID}
	for _, hit := range result.Page.Hits {
		golden.Hits = append(golden.Hits, hit.ArtifactID+":"+hit.Digest+":"+hit.Title)
	}
	return golden
}

// driveToCompletion runs passes until the job reaches a terminal state.
func driveToCompletion(t *testing.T, ctx context.Context, executor *indexerapp.RebuildExecutor, jobID string) indexerapp.RebuildJobView {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		if _, err := executor.RunPass(ctx); err != nil && ctx.Err() == nil {
			t.Fatalf("rebuild pass: %v", err)
		}
		job, err := executor.GetJob(context.Background(), jobID)
		if err != nil {
			t.Fatalf("read job: %v", err)
		}
		switch job.State {
		case "completed":
			return job
		case "failed", "canceled":
			t.Fatalf("job ended %s: %s", job.State, job.FailureCategory)
		}
	}
	t.Fatal("rebuild never completed")
	return indexerapp.RebuildJobView{}
}

func TestProjectKnowledgeRebuildGoldenCrashResumeAndDestroyRestore(t *testing.T) {
	t.Parallel()
	f := newRebuildFixture(t)
	ctx := context.Background()

	if _, err := f.proj.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Seed the projection the way the live consumer would have, using the
	// real reconciliation pass over the fake feed.
	fake := &fakeRebuildFeed{pool: f.pool}
	reconcileIDs := ids.UUIDv7{}
	reconcileAdapter := &portsCoreFeedAdapter{fake: fake, proj: f.proj}
	if err := indexerapp.Reconcile(ctx, reconcileAdapter, f.proj, reconcileIDs, 100); err != nil {
		t.Fatalf("initial reconciliation: %v", err)
	}

	search := indexerapp.NewSearchServiceForTest(f.proj)
	ownerA := f.owner
	ownerB := f.foreignOwner

	// The pre-rebuild golden: only active projects, only their owners.
	goldenAlpha := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000942", ownerA, "rebuild-unique-alpha")
	goldenGamma := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000944", ownerA, "rebuild-unique-gamma")
	if len(goldenAlpha.Hits) != 1 || len(goldenGamma.Hits) != 1 {
		t.Fatalf("seeded goldens: %+v %+v", goldenAlpha, goldenGamma)
	}
	foreign := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000942", ownerB, "rebuild-unique-alpha")
	if len(foreign.Hits) != 0 {
		t.Fatalf("foreign owner saw hits: %+v", foreign)
	}
	archived := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000945", ownerA, "rebuild-unique-archived")
	if len(archived.Hits) != 0 {
		t.Fatalf("archived project was searchable: %+v", archived)
	}

	executor, _ := f.buildExecutor(t)
	job, _, err := executor.Start(ctx, indexerapp.RebuildRequest{
		Scope: "all", IdempotencyKey: "rebuild-golden-key",
	})
	if err != nil {
		t.Fatalf("start rebuild: %v", err)
	}

	// Crash windows: advance one pass with one executor instance, then let a
	// FRESH instance (restart) resume from the durable phase. At most one
	// promotion may ever land.
	if _, err := executor.RunPass(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := f.buildExecutor(t)
	done := driveToCompletion(t, ctx, restarted, job.ID)
	if done.State != "completed" {
		t.Fatalf("job state %s", done.State)
	}
	// Exactly one active generation must exist.
	var activeCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_index.active_generation`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("active generation rows = %d, want 1", activeCount)
	}

	// The post-rebuild golden must equal the pre-rebuild golden: promotion is
	// transparent to correct searches.
	afterAlpha := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000942", ownerA, "rebuild-unique-alpha")
	afterGamma := f.captureGolden(t, search, "01999999-9999-7999-8999-000000000944", ownerA, "rebuild-unique-gamma")
	if strings.Join(afterAlpha.Hits, "|") != strings.Join(goldenAlpha.Hits, "|") ||
		strings.Join(afterGamma.Hits, "|") != strings.Join(goldenGamma.Hits, "|") {
		t.Fatalf("golden drifted: %+v vs %+v; %+v vs %+v", afterAlpha, goldenAlpha, afterGamma, goldenGamma)
	}

	// Idempotency: same key + same scope replays the same job; same key +
	// different scope conflicts.
	replayed, created, err := executor.Start(ctx, indexerapp.RebuildRequest{Scope: "all", IdempotencyKey: "rebuild-golden-key"})
	if err != nil || created {
		t.Fatalf("same-key replay created=%v err=%v", created, err)
	}
	if replayed.ID != job.ID {
		t.Fatalf("replay returned job %s, want %s", replayed.ID, job.ID)
	}
	if _, _, err := executor.Start(ctx, indexerapp.RebuildRequest{Scope: "project", OwnerUserID: ownerA, ProjectID: "01999999-9999-7999-8999-000000000942", IdempotencyKey: "rebuild-golden-key"}); err != indexerapp.ErrRebuildConflict {
		t.Fatalf("same-key different scope = %v, want conflict", err)
	}

	// Cancel safety: a project rebuild canceled at checkpoint terminates
	// canceled and never promotes.
	canceled, _, err := executor.Start(ctx, indexerapp.RebuildRequest{
		Scope: "project", OwnerUserID: ownerA,
		ProjectID: "01999999-9999-7999-8999-000000000942", IdempotencyKey: "rebuild-cancel-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := executor.Cancel(ctx, canceled.ID)
	if err != nil || !accepted {
		t.Fatalf("cancel accepted=%v err=%v", accepted, err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		job, err := executor.GetJob(ctx, canceled.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == "canceled" {
			break
		}
		_, _ = executor.RunPass(ctx)
	}
	final, err := executor.GetJob(ctx, canceled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "canceled" {
		t.Fatalf("cancel job state = %s, want canceled", final.State)
	}

	// Disaster recovery: destroy the entire indexer-owned projection in this
	// temporary database, re-run the migration, and rebuild --all from Core
	// authority. The search golden must be re-established exactly. The
	// Core-owned rows are read back and verified untouched.
	var artifactDigest string
	if err := f.pool.QueryRow(ctx, `SELECT digest FROM workos_core.project_review_artifacts WHERE id = '01999999-9999-7999-8999-000000000950'`).Scan(&artifactDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DROP SCHEMA workos_index CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM workos_meta.schema_migrations WHERE name = '027_index_projection.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, f.dsn); err != nil {
		t.Fatalf("re-migrate after destroy: %v", err)
	}
	pool2, err := pgxpool.New(ctx, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	projection2, err := indexerpostgres.New(pool2, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection2.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	search2 := indexerapp.NewSearchServiceForTest(projection2)
	reconciled := &portsCoreFeedAdapter{fake: fake, proj: projection2}
	if err := indexerapp.Reconcile(ctx, reconciled, projection2, reconcileIDs, 100); err != nil {
		t.Fatalf("post-destroy reconciliation: %v", err)
	}
	executor2, _ := f.buildExecutorOn(t, pool2, projection2)
	job2, _, err := executor2.Start(ctx, indexerapp.RebuildRequest{Scope: "all", IdempotencyKey: "rebuild-restore-key"})
	if err != nil {
		t.Fatal(err)
	}
	restarted2, _ := f.buildExecutorOn(t, pool2, projection2)
	driveToCompletion(t, ctx, restarted2, job2.ID)
	afterRestore := f.captureGolden(t, search2, "01999999-9999-7999-8999-000000000942", ownerA, "rebuild-unique-alpha")
	if strings.Join(afterRestore.Hits, "|") != strings.Join(goldenAlpha.Hits, "|") {
		t.Fatalf("restore golden drifted: %+v vs %+v", afterRestore, goldenAlpha)
	}
	var restoredDigest string
	if err := pool2.QueryRow(ctx, `SELECT digest FROM workos_core.project_review_artifacts WHERE id = '01999999-9999-7999-8999-000000000950'`).Scan(&restoredDigest); err != nil {
		t.Fatal(err)
	}
	if restoredDigest != artifactDigest {
		t.Fatal("core artifact digest changed across the destroy/restore")
	}
}
