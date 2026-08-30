//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/migrations"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	surfacedomain "github.com/yangtao121/workos/internal/runtime/surface/domain"
	surfaceports "github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// The 006/007 checksums are pinned: both migrations have already run on the
// acceptance volume and are checksum-protected, so any later edit is a hard
// failure even before migrate.Run rejects it.
const (
	webBundle006Checksum = "628cc5099617c078352612b20bee3f83cefb166a8e5e25ea386da61da317cc27"
	surface007Checksum   = "b3fed6b62cbcd6af4d29f73076e83940393e79fd6351f2acaafdf909ec34a986"
	installationGrant008 = "180ba05df3c54c45d16dd1c67f8b45cacdde8d6ac1a77ae5338abc3dd0055766"
	appTaskProvenance009 = "233ea77ca9f3dc0d18362c0cc2a650eb288c5bc90d0c0e01e3ec9428b6f411db"
	bridgeToken010       = "91f47007a071915e0d6c2b39f35f2611f2b1f30c72781d113fd801368045896a"
	mutableGrants011     = "1b85383b53f23829151cacca44c5f400f1fb9ca1e06f4836767a3c40f354775f"
	surfaceGrant012      = "9b8335b1a7936ef96b5b5aaeeeac8b351768bb5c98152bfed6d80bbd904bcc89"
)

// TestAllMigrationChecksumsArePinned pins every shipped migration file
// byte-for-byte, so editing history anywhere in 001..012 fails here first.
func TestAllMigrationChecksumsArePinned(t *testing.T) {
	t.Parallel()
	pinned := map[string]string{
		"001_foundation.sql":                             "f748516e52ae915e0582a8dfa5de665a6590264268d0e55c46659746bcaa0378",
		"002_app_registry.sql":                           appRegistry002Checksum,
		"003_app_registry_idempotency.sql":               "73766b95799bce3e0f4569e49940df044fd287ae723f38ed7f410c719e83ebe3",
		"004_project_app_installations.sql":              installation004Checksum,
		"005_project_app_installation_request_owner.sql": "45cb2bb4abb590656cb119e0517af3a220f94d279c43bec1eec754c5bf0a8781",
		"006_web_bundle_artifacts.sql":                   webBundle006Checksum,
		"007_surface_sessions.sql":                       surface007Checksum,
		"008_project_installation_grants.sql":            installationGrant008,
		"009_agent_app_task_provenance.sql":              appTaskProvenance009,
		"010_surface_bridge_tokens.sql":                  bridgeToken010,
		"011_mutable_project_app_grants.sql":             mutableGrants011,
		"012_surface_grant_revision.sql":                 surfaceGrant012,
		"013_project_create_requests.sql":                "18de5d8271d669cbc7ca1aa0440927f792a1b555dc96208507369d8942691210",
		"014_agent_app_policy_quota.sql":                 "dd92c010d10192c432cb17d2b75222fae608c7bfbde79d240917c4eb7aa65f4a",
		// 015–019 are immutable supervised-workload history. Behavioral
		// corrections ship only as new forward migrations.
		"015_runtime_workloads.sql":                "5920e2ed23dd3b68cd79cc92d6244f0e275a288373887f90b4df19519f996775",
		"016_reliability_incidents.sql":            "468f0d888b31bb0fff6a5c6c84129bb6063ee9bfad6338834621204236280f9c",
		"017_incident_acknowledge_keys.sql":        "df5d24aab6ea0dff99c665b5f5c0fe9acb086a9e1db3e63b86e7eb68e03544d0",
		"018_runtime_workload_convergence.sql":     "b685e4a5a35285b67a0f1011fccc6de7a34d22e4ba3bd024f959215b9ccd7331",
		"019_reliability_incident_convergence.sql": "08d030c19f233a8a47b793c82aabbb1667b9f460f40e283e926e162fa6ca3709",
	}
	for name, want := range pinned {
		if got := migrationFileChecksum(t, name); got != want {
			t.Errorf("migration %s checksum changed: got %s want %s", name, got, want)
		}
	}
}

// TestWebBundleMigrationsFromEmptyDatabase proves 006/007 apply forward-only
// on a pristine database and create exactly the module-owned facts with
// their constraints.
func TestWebBundleMigrationsFromEmptyDatabase(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations from empty database: %v", err)
	}
	// A second run is a no-op.
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("second migrations run: %v", err)
	}
	conn, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, table := range []string{"web_bundle_artifacts", "web_bundle_files", "web_bundle_artifact_requests"} {
		var exists bool
		if err := conn.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'workos_core' AND table_name = $1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing: %v %v", table, err, exists)
		}
	}
	for _, table := range []string{"surface_sessions", "surface_session_requests"} {
		var exists bool
		if err := conn.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'workos_runtime' AND table_name = $1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("runtime table %s missing: %v %v", table, err, exists)
		}
	}

	t.Run("ArtifactConstraints", func(t *testing.T) {
		if _, err := conn.Exec(ctx, `INSERT INTO workos_core.web_bundle_artifacts (
			id, owner_user_id, type, title, media_type, content_ref, digest, entrypoint, file_count, total_size_bytes, created_at
		) VALUES (
			$1, $1, 'custom.type', 't', 'application/vnd.workos.web-bundle.v1', $2, 'sha256:' || repeat('a', 64), 'index.html', 1, 1, now()
		)`, newUUIDForTest(31), "wbbnd:"+newUUIDForTest(32)); err == nil {
			t.Fatal("non-web-bundle type accepted")
		}
		if _, err := conn.Exec(ctx, `INSERT INTO workos_core.web_bundle_artifacts (
			id, owner_user_id, type, title, media_type, content_ref, digest, entrypoint, file_count, total_size_bytes, created_at
		) VALUES (
			$1, $1, 'app.web-bundle.v1', 't', 'text/html', 'wbbnd:' || $2, 'sha256:' || repeat('a', 64), 'index.html', 1, 1, now()
		)`, newUUIDForTest(33), newUUIDForTest(34)); err == nil {
			t.Fatal("wrong bundle media type accepted")
		}
		if _, err := conn.Exec(ctx, `INSERT INTO workos_core.web_bundle_files (artifact_id, path, media_type, size_bytes, digest, content)
			VALUES ($1, '../escape.js', 'text/javascript', 1, 'sha256:' || repeat('a', 64), 'x'::bytea)`, newUUIDForTest(35)); err == nil {
			t.Fatal("traversal path accepted by the files table")
		}
	})

	t.Run("SurfaceSessionConstraints", func(t *testing.T) {
		id := newUUIDForTest(36)
		owner := newUUIDForTest(37)
		device := newUUIDForTest(38)
		insert := func(renderer, path string, expiresOffset string) error {
			_, err := conn.Exec(ctx, fmt.Sprintf(`INSERT INTO workos_runtime.surface_sessions (
				id, owner_user_id, device_id, idempotency_key, request_digest, project_id, app_instance_id, renderer,
				app_id, app_version, manifest_digest, artifact_id, artifact_digest, entrypoint, path, created_at, expires_at
			) VALUES ($1, $2, $3, 'k', 'sha256:' || repeat('a', 64), $4, $4, '%s', 'notes-app', '1.0.0',
				'sha256:' || repeat('a', 64), $4, 'sha256:' || repeat('a', 64), 'index.html', '%s', now(), now() + interval '%s')`,
				renderer, path, expiresOffset), id, owner, device, newUUIDForTest(39))
			return err
		}
		if err := insert("web-service", "/surfaces/"+id+"/", "15 minutes"); err == nil {
			t.Fatal("unsupported renderer accepted")
		}
		if err := insert("web-bundle", "/elsewhere/", "15 minutes"); err == nil {
			t.Fatal("foreign path shape accepted")
		}
		if err := insert("web-bundle", "/surfaces/"+id+"/", "-1 minute"); err == nil {
			t.Fatal("expiry before creation accepted")
		}
		if err := insert("web-bundle", "/surfaces/"+id+"/", "15 minutes"); err != nil {
			t.Fatalf("valid session row rejected: %v", err)
		}
		// Closed before created is rejected.
		if _, err := conn.Exec(ctx, `UPDATE workos_runtime.surface_sessions SET closed_at = created_at - interval '1 minute'`); err == nil {
			t.Fatal("closed_at before created_at accepted")
		}
	})
}

// TestWebBundleMigrationsAppliedToAcceptanceVolume asserts the acceptance
// volume received 006/007 through the normal bootstrap path.
func TestWebBundleMigrationsAppliedToAcceptanceVolume(t *testing.T) {
	t.Parallel()
	conn := appRegistryDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range []string{"006_web_bundle_artifacts.sql", "007_surface_sessions.sql"} {
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workos_meta.schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			t.Skipf("acceptance database unavailable: %v", err)
		}
		if !applied {
			t.Fatalf("%s must be applied on the acceptance volume", name)
		}
	}
	var schemaExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'workos_runtime')`).Scan(&schemaExists); err != nil || !schemaExists {
		t.Fatalf("workos_runtime schema missing on acceptance volume: %v %v", err, schemaExists)
	}
}

// TestArtifactRepositoryConcurrency proves the PostgreSQL idempotency
// arbitration with two independent repository instances on one scratch
// database: one fact per key, replays of the winner, conflicts for losers
// with different requests.
func TestArtifactRepositoryConcurrency(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	leftPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer leftPool.Close()
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rightPool.Close()

	owner := newUUIDForTest(41)
	if _, err := leftPool.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'artifact race', now())`, owner); err != nil {
		t.Fatal(err)
	}

	// Identifiers are derived per call from an atomic sequence so the command
	// closure itself is race-free when two goroutines race the same key; the
	// whole point is to race the repository, not the test code.
	var artifactSequence atomic.Int64
	command := func(repository *artifactpostgres.Repository, key, title string) (artifactdomain.Artifact, error) {
		bundle, err := artifactdomain.NormalizeWebBundle("index.html", []artifactdomain.BundleFileInput{
			{Path: "index.html", Content: []byte("<!doctype html><title>" + title + "</title>")},
			{Path: "app.js", Content: []byte("console.log(1)")},
		})
		if err != nil {
			return artifactdomain.Artifact{}, err
		}
		now := time.Now().UTC()
		sequence := artifactSequence.Add(1)
		record := artifactdomain.Artifact{
			ID: newUUIDForTest(100 + int(sequence)), OwnerUserID: owner, Type: artifactdomain.TypeWebBundle,
			Title: title, MediaType: artifactdomain.MediaTypeBundle, ContentRef: "wbbnd:" + newUUIDForTest(200+int(sequence)),
			Digest: bundle.CanonicalDigest(), Entrypoint: bundle.Entrypoint,
			FileCount: len(bundle.Files), TotalSizeBytes: 1024, CreatedAt: now,
		}
		return repository.Create(ctx, artifactports.CreateCommand{
			Artifact: record, Bundle: bundle, IdempotencyKey: key,
			RequestDigest: artifactdomain.CreateRequestDigest(title, record.Digest),
		})
	}

	t.Run("TwoRepositoriesSameKeyProduceOneFact", func(t *testing.T) {
		key := fmt.Sprintf("race-%d", time.Now().UnixNano())
		left := artifactpostgres.New(leftPool)
		right := artifactpostgres.New(rightPool)
		start := make(chan struct{})
		results := make(chan artifactdomain.Artifact, 2)
		failures := make(chan error, 2)
		var group sync.WaitGroup
		for _, repository := range []*artifactpostgres.Repository{left, right} {
			group.Add(1)
			go func(repository *artifactpostgres.Repository) {
				defer group.Done()
				<-start
				artifact, err := command(repository, key, "Race Bundle")
				if err != nil {
					failures <- err
					return
				}
				results <- artifact
			}(repository)
		}
		close(start)
		group.Wait()
		close(results)
		close(failures)
		// Serialization decides the shape: the loser either replays the stored
		// fact (both succeed with the same artifact) or aborts as a conflict;
		// either way exactly one artifact fact and one request row exist.
		var ids []string
		for artifact := range results {
			ids = append(ids, artifact.ID)
		}
		if len(ids) == 2 && ids[0] != ids[1] {
			t.Fatalf("same-key race produced two different artifacts: %v", ids)
		}
		if len(ids) == 0 {
			t.Fatalf("same-key race produced no winner: %v", collectErrors(failures))
		}
		var rows int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_artifacts`).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("same-key race created %d artifact rows (err %v)", rows, err)
		}
		var requests int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_artifact_requests WHERE idempotency_key = $1`, key).Scan(&requests); err != nil || requests != 1 {
			t.Fatalf("same-key race created %d request rows (err %v)", requests, err)
		}
	})

	t.Run("DifferentRequestUnderSameKeyConflicts", func(t *testing.T) {
		key := fmt.Sprintf("conflict-%d", time.Now().UnixNano())
		left := artifactpostgres.New(leftPool)
		if _, err := command(left, key, "First"); err != nil {
			t.Fatal(err)
		}
		if _, err := command(left, key, "Second"); err != artifactdomain.ErrIdempotencyConflict {
			t.Fatalf("different request under the same key must abort, got %v", err)
		}
	})
}

// TestSurfaceSessionRepositoryDurability proves session facts survive a
// fresh repository instance (the restart equivalent at the storage layer):
// active sessions keep serving metadata, closed sessions stay closed, and
// the create key replays the first snapshot.
func TestSurfaceSessionRepositoryDurability(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	first, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	repository := surfacepostgres.New(first)

	owner, device := newUUIDForTest(51), newUUIDForTest(52)
	now := time.Now().UTC()
	descriptor := surfacedomain.LaunchDescriptor{
		AppID: "notes-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactID:     newUUIDForTest(53), ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		Entrypoint: "index.html",
	}
	session := surfacedomain.SurfaceSession{
		ID: newUUIDForTest(54), OwnerUserID: owner, DeviceID: device,
		IdempotencyKey: "durable-key", RequestDigest: "sha256:" + strings.Repeat("c", 64),
		ProjectID: newUUIDForTest(55), AppInstanceID: newUUIDForTest(56),
		Renderer: surfacedomain.RendererWebBundle, Descriptor: descriptor,
		InstallationGrantRevision: 1,
		Path:                      "", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	session.Path = surfacedomain.SessionPath(session.ID)
	created, err := repository.Create(ctx, surfaceports.CreateSessionCommand{
		Session: session, IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Close(ctx, owner, device, created.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	// A brand-new repository instance (process restart) sees the same facts.
	secondPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer secondPool.Close()
	restarted := surfacepostgres.New(secondPool)
	stored, err := restarted.GetSession(ctx, owner, device, created.ID)
	if err != nil || stored.ClosedAt == nil {
		t.Fatalf("closed state lost across instances: %v %+v", err, stored)
	}
	if active, err := restarted.GetActiveSession(ctx, owner, device, created.ID, time.Now().UTC()); err != surfacedomain.ErrNotFound {
		t.Fatalf("closed session must not be active: %v %+v", err, active)
	}
	storedRequest, found, err := restarted.LookupRequest(ctx, owner, "durable-key")
	if err != nil || !found || storedRequest.RequestDigest != session.RequestDigest || storedRequest.SessionID != created.ID {
		t.Fatalf("create key mapping lost across instances: %+v found=%v %v", storedRequest, found, err)
	}
	foreign, err := restarted.GetSession(ctx, owner, newUUIDForTest(57), created.ID)
	if err != surfacedomain.ErrNotFound || foreign.ID != "" {
		t.Fatalf("foreign device must not read the session: %v %+v", err, foreign)
	}
}

// TestSurfaceSessionRepositoryConcurrency proves the runtime session
// arbitration against real PostgreSQL with two independent pools (the
// restart/race equivalent at the storage layer): same-key/same-request races
// converge on one session fact, same-key/different-request races produce
// exactly one loser with zero orphan rows, and the trusted device is bound
// into the idempotency ruling.
func TestSurfaceSessionRepositoryConcurrency(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	leftPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer leftPool.Close()
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rightPool.Close()
	left, right := surfacepostgres.New(leftPool), surfacepostgres.New(rightPool)

	owner, deviceA, deviceB := newUUIDForTest(61), newUUIDForTest(62), newUUIDForTest(63)
	project, appInstance := newUUIDForTest(64), newUUIDForTest(65)
	now := time.Now().UTC()
	requestDigest := "sha256:" + strings.Repeat("a", 64)
	otherDigest := "sha256:" + strings.Repeat("d", 64)
	// The application digests the trusted device into the canonical create
	// request, so the same owner/key from another device arrives with a
	// different digest.
	deviceADigest := "sha256:" + strings.Repeat("1", 64)
	deviceBDigest := "sha256:" + strings.Repeat("2", 64)

	newSession := func(id int, device string) surfacedomain.SurfaceSession {
		session := surfacedomain.SurfaceSession{
			ID: newUUIDForTest(id), OwnerUserID: owner, DeviceID: device,
			IdempotencyKey: "concurrent-key", RequestDigest: requestDigest,
			ProjectID: project, AppInstanceID: appInstance,
			Renderer: surfacedomain.RendererWebBundle,
			Descriptor: surfacedomain.LaunchDescriptor{
				AppID: "notes-app", Version: "1.0.0",
				ManifestDigest: "sha256:" + strings.Repeat("a", 64),
				ArtifactID:     newUUIDForTest(66), ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
				Entrypoint: "index.html",
			},
			InstallationGrantRevision: 1,
			CreatedAt:                 now, ExpiresAt: now.Add(15 * time.Minute),
		}
		session.Path = surfacedomain.SessionPath(session.ID)
		return session
	}

	t.Run("SameKeySameRequestRaceProducesOneFact", func(t *testing.T) {
		start := make(chan struct{})
		var group sync.WaitGroup
		results := make(chan surfacedomain.SurfaceSession, 2)
		failures := make(chan error, 2)
		sessions := map[string]surfacedomain.SurfaceSession{
			"left": newSession(71, deviceA), "right": newSession(72, deviceA),
		}
		for _, side := range []string{"left", "right"} {
			group.Add(1)
			go func(side string) {
				defer group.Done()
				<-start
				repository := left
				if side == "right" {
					repository = right
				}
				session, err := repository.Create(ctx, surfaceports.CreateSessionCommand{
					Session: sessions[side], IdempotencyKey: "concurrent-key", RequestDigest: requestDigest,
				})
				if err != nil {
					failures <- err
					return
				}
				results <- session
			}(side)
		}
		close(start)
		group.Wait()
		close(results)
		close(failures)
		var ids []string
		for session := range results {
			ids = append(ids, session.ID)
		}
		if len(ids) == 0 {
			t.Fatalf("same-key race produced no winner: %v", collectErrors(failures))
		}
		for _, id := range ids {
			if id != ids[0] {
				t.Fatalf("same-key race returned two different sessions: %v", ids)
			}
		}
		var sessionRows, requestRows int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.surface_sessions`).Scan(&sessionRows); err != nil || sessionRows != 1 {
			t.Fatalf("same-key race left %d session rows (err %v)", sessionRows, err)
		}
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.surface_session_requests`).Scan(&requestRows); err != nil || requestRows != 1 {
			t.Fatalf("same-key race left %d mapping rows (err %v)", requestRows, err)
		}
	})

	t.Run("SameKeyDifferentRequestRaceLeavesNoOrphan", func(t *testing.T) {
		// The winner's digest is whichever create consumed the key first; the
		// loser must abort and roll back its whole session insert.
		start := make(chan struct{})
		var group sync.WaitGroup
		outcomes := make(chan error, 2)
		leftSession, rightSession := newSession(73, deviceA), newSession(74, deviceA)
		leftSession.IdempotencyKey = "conflict-key"
		rightSession.IdempotencyKey = "conflict-key"
		rightSession.RequestDigest = otherDigest
		for _, candidate := range []surfacedomain.SurfaceSession{leftSession, rightSession} {
			group.Add(1)
			go func(candidate surfacedomain.SurfaceSession) {
				defer group.Done()
				<-start
				repository := left
				if candidate.RequestDigest == otherDigest {
					repository = right
				}
				_, err := repository.Create(ctx, surfaceports.CreateSessionCommand{
					Session: candidate, IdempotencyKey: "conflict-key", RequestDigest: candidate.RequestDigest,
				})
				outcomes <- err
			}(candidate)
		}
		close(start)
		group.Wait()
		close(outcomes)
		verdicts := []string{}
		for err := range outcomes {
			switch {
			case err == nil:
				verdicts = append(verdicts, "won")
			case errors.Is(err, surfacedomain.ErrIdempotencyConflict):
				verdicts = append(verdicts, "aborted")
			default:
				t.Fatalf("unexpected race verdict: %v", err)
			}
		}
		sort.Strings(verdicts)
		if strings.Join(verdicts, ",") != "aborted,won" {
			t.Fatalf("different-request race must yield exactly one winner and one abort, got %v", verdicts)
		}
		var sessionRows int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.surface_sessions`).Scan(&sessionRows); err != nil {
			t.Fatal(err)
		}
		// Exactly the winner's session remains: the loser rolled back its
		// insert inside the create transaction.
		if sessionRows != 2 { // one from SameKeySameRequestRace + one winner
			t.Fatalf("conflict race left %d session rows, orphan suspected", sessionRows)
		}
		var requestRows int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.surface_session_requests WHERE idempotency_key = 'conflict-key'`).Scan(&requestRows); err != nil || requestRows != 1 {
			t.Fatalf("conflict race left %d conflict-key mappings (err %v)", requestRows, err)
		}
	})

	t.Run("SameKeyFromAnotherTrustedDeviceAborts", func(t *testing.T) {
		deviceSession := newSession(75, deviceA)
		deviceSession.IdempotencyKey = "device-key"
		if _, err := left.Create(ctx, surfaceports.CreateSessionCommand{
			Session: deviceSession, IdempotencyKey: "device-key", RequestDigest: deviceADigest,
		}); err != nil {
			t.Fatal(err)
		}
		// The application digests the trusted device into the canonical
		// request, so the other trusted device arrives with a different
		// digest under the same key: the stored digest rules a stable abort,
		// not a session-lookup miss.
		other := newSession(76, deviceB)
		other.IdempotencyKey = "device-key"
		if _, err := right.Create(ctx, surfaceports.CreateSessionCommand{
			Session: other, IdempotencyKey: "device-key", RequestDigest: deviceBDigest,
		}); !errors.Is(err, surfacedomain.ErrIdempotencyConflict) {
			t.Fatalf("other trusted device verdict %v, want ErrIdempotencyConflict", err)
		}
		var deviceBRows int
		if err := leftPool.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.surface_sessions WHERE device_id = $1`, deviceB).Scan(&deviceBRows); err != nil || deviceBRows != 0 {
			t.Fatalf("aborted device left %d rows (err %v)", deviceBRows, err)
		}
		// The device-bound digest replays exactly for the owning device even
		// after close.
		if _, err := left.Close(ctx, owner, deviceA, deviceSession.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		replayed, err := left.Create(ctx, surfaceports.CreateSessionCommand{
			Session: newSession(77, deviceA), IdempotencyKey: "device-key", RequestDigest: deviceADigest,
		})
		if err != nil || replayed.ID != deviceSession.ID || replayed.ClosedAt == nil {
			t.Fatalf("device-bound replay after close failed: %v %+v", err, replayed)
		}
		// A different key on the second device creates independently.
		fresh := newSession(78, deviceB)
		fresh.IdempotencyKey = "device-key-b"
		if _, err := right.Create(ctx, surfaceports.CreateSessionCommand{
			Session: fresh, IdempotencyKey: "device-key-b", RequestDigest: deviceBDigest,
		}); err != nil {
			t.Fatalf("independent key on the second device failed: %v", err)
		}
	})
}

// TestSurfaceSessionCreateCloseAssetRace races a repository Close against an
// active-session read through a barrier (no sleeps). Either interleaving is
// a legitimate linearization; after Close returns, the session must fail
// closed for asset serving.
func TestSurfaceSessionCreateCloseAssetRace(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := surfacepostgres.New(pool)

	owner, device := newUUIDForTest(81), newUUIDForTest(82)
	now := time.Now().UTC()
	session := surfacedomain.SurfaceSession{
		ID: newUUIDForTest(83), OwnerUserID: owner, DeviceID: device,
		IdempotencyKey: "race-close", RequestDigest: "sha256:" + strings.Repeat("a", 64),
		ProjectID: newUUIDForTest(84), AppInstanceID: newUUIDForTest(85),
		Renderer: surfacedomain.RendererWebBundle,
		Descriptor: surfacedomain.LaunchDescriptor{
			AppID: "notes-app", Version: "1.0.0",
			ManifestDigest: "sha256:" + strings.Repeat("a", 64),
			ArtifactID:     newUUIDForTest(86), ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
			Entrypoint: "index.html",
		},
		InstallationGrantRevision: 1,
		CreatedAt:                 now, ExpiresAt: now.Add(15 * time.Minute),
	}
	session.Path = surfacedomain.SessionPath(session.ID)
	if _, err := repository.Create(ctx, surfaceports.CreateSessionCommand{
		Session: session, IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
	}); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	start := make(chan struct{})
	served := make(chan error, 1)
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		_, err := repository.GetActiveSession(ctx, owner, device, session.ID, time.Now().UTC())
		served <- err
	}()
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		if _, err := repository.Close(ctx, owner, device, session.ID, time.Now().UTC()); err != nil {
			t.Errorf("close failed: %v", err)
		}
	}()
	close(start)
	group.Wait()
	if err := <-served; err != nil && !errors.Is(err, surfacedomain.ErrNotFound) {
		t.Fatalf("in-flight read verdict: %v", err)
	}
	// Post-close reads always fail closed.
	if _, err := repository.GetActiveSession(ctx, owner, device, session.ID, time.Now().UTC()); !errors.Is(err, surfacedomain.ErrNotFound) {
		t.Fatalf("asset read after close returned: %v", err)
	}
}

// TestArtifactCreateRollsBackMidTransaction injects a real PostgreSQL
// arbitration failure into the middle of the artifact create transaction:
// the concurrent loser has already inserted its metadata and file rows when
// the request-mapping primary key rejects it, so the rolled-back transaction
// must leave none of them — and the winner's key must still replay.
func TestArtifactCreateRollsBackMidTransaction(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := artifactpostgres.New(pool)

	owner := newUUIDForTest(91)
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'artifact rollback', now())`, owner); err != nil {
		t.Fatal(err)
	}

	command := func(key string, marker string, sequence int) artifactports.CreateCommand {
		bundle, err := artifactdomain.NormalizeWebBundle("index.html", []artifactdomain.BundleFileInput{
			{Path: "index.html", Content: []byte("<!doctype html><title>" + marker + "</title>")},
			{Path: "app.js", Content: []byte("// " + marker)},
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		record := artifactdomain.Artifact{
			ID: newUUIDForTest(100 + sequence), OwnerUserID: owner, Type: artifactdomain.TypeWebBundle,
			Title: "Rollback Bundle", MediaType: artifactdomain.MediaTypeBundle,
			ContentRef: "wbbnd:" + newUUIDForTest(150+sequence),
			Digest:     bundle.CanonicalDigest(), Entrypoint: bundle.Entrypoint,
			FileCount: len(bundle.Files), TotalSizeBytes: 1024, CreatedAt: now,
		}
		return artifactports.CreateCommand{
			Artifact: record, Bundle: bundle, IdempotencyKey: key,
			RequestDigest: artifactdomain.CreateRequestDigest(record.Title, record.Digest),
		}
	}

	winnerCommand := command("rollback-key", "winner", 1)
	loserCommand := command("rollback-key", "loser", 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, candidate := range []artifactports.CreateCommand{winnerCommand, loserCommand} {
		group.Add(1)
		go func(candidate artifactports.CreateCommand) {
			defer group.Done()
			<-start
			_, err := repository.Create(ctx, candidate)
			outcomes <- err
		}(candidate)
	}
	close(start)
	group.Wait()
	close(outcomes)
	// Either candidate may win the race; the other must abort cleanly.
	won, aborted := 0, 0
	for err := range outcomes {
		switch {
		case err == nil:
			won++
		case errors.Is(err, artifactdomain.ErrIdempotencyConflict):
			aborted++
		default:
			t.Fatalf("unexpected rollback verdict: %v", err)
		}
	}
	if won != 1 || aborted != 1 {
		t.Fatalf("rollback race must yield one winner and one conflict, got won=%d aborted=%d", won, aborted)
	}

	// Exactly one artifact fact survives with its two files and its request
	// mapping; the loser's metadata, files, and mapping rolled back with the
	// transaction.
	var artifacts, files, requests int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_artifacts`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_artifact_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if artifacts != 1 || files != 2 || requests != 1 {
		t.Fatalf("mid-transaction rollback left artifacts=%d files=%d requests=%d", artifacts, files, requests)
	}
	// The winner's digest is whichever candidate owns the surviving row; the
	// loser's digest must be gone entirely.
	var winnerDigest string
	if err := pool.QueryRow(ctx, `SELECT digest FROM workos_core.web_bundle_artifacts`).Scan(&winnerDigest); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []artifactports.CreateCommand{winnerCommand, loserCommand} {
		if candidate.Artifact.Digest == winnerDigest {
			continue
		}
		var orphanArtifacts, orphanFiles int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_artifacts WHERE digest = $1`, candidate.Artifact.Digest).Scan(&orphanArtifacts); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.web_bundle_files WHERE artifact_id = $1`, candidate.Artifact.ID).Scan(&orphanFiles); err != nil {
			t.Fatal(err)
		}
		if orphanArtifacts != 0 || orphanFiles != 0 {
			t.Fatalf("loser left orphan rows: artifacts=%d files=%d", orphanArtifacts, orphanFiles)
		}
	}
	// The winning request replays exactly.
	var winner artifactports.CreateCommand
	if winnerDigest == winnerCommand.Artifact.Digest {
		winner = winnerCommand
	} else {
		winner = loserCommand
	}
	replayed, err := repository.Create(ctx, winner)
	if err != nil || replayed.Digest != winnerDigest {
		t.Fatalf("winner replay failed: %v", err)
	}
}

func collectErrors(channel chan error) []error {
	result := []error{}
	for err := range channel {
		result = append(result, err)
	}
	return result
}
