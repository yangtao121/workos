//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// stubAppTaskGateway satisfies the AppAgentService task dependency; the
// ingest authorization path never touches tasks.
type stubAppTaskGateway struct{}

func (stubAppTaskGateway) SubmitForApp(ctx context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	return agentdomain.Task{}, errors.New("not used")
}

func (stubAppTaskGateway) GetAppTaskByIdempotency(ctx context.Context, ownerUserID, appInstanceID, clientKey string) (agentdomain.Task, string, bool, error) {
	return agentdomain.Task{}, "", false, errors.New("not used")
}

func (stubAppTaskGateway) GetAppTask(ctx context.Context, ownerUserID, appInstanceID, taskID string) (agentdomain.Task, string, error) {
	return agentdomain.Task{}, "", errors.New("not used")
}

func (stubAppTaskGateway) AppTaskEvents(ctx context.Context, ownerUserID, appInstanceID, taskID string, after int64, limit int) ([]agentdomain.Event, error) {
	return nil, errors.New("not used")
}

// stubAppCatalog resolves one pinned fixture app version.
type stubAppCatalog struct{}

func (stubAppCatalog) Resolve(ctx context.Context, ownerUserID, appID, version string) (projectdomain.PinnedApp, error) {
	return projectdomain.PinnedApp{
		AppID: appID, Version: version,
		ManifestDigest: "sha256:" + ids.UUIDv7{}.New(),
		Scope:          "project",
		Permissions:    []string{"notifications.create", "knowledge.read"},
	}, nil
}

// pausingAppNotificationAuthorizer exposes the instant after the real
// transaction-scoped authorizer has acquired its project/installation share
// locks. Tests can then prove a revocation write waits for the authorized
// notification transaction instead of racing its verdict.
type pausingAppNotificationAuthorizer struct {
	inner   notificationapp.AppInstallationAuthorizer
	locked  chan struct{}
	release chan struct{}
}

func (p *pausingAppNotificationAuthorizer) AuthorizeAppNotificationTx(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64) (notificationports.AppInstallationFacts, error) {
	facts, err := p.inner.AuthorizeAppNotificationTx(ctx, tx, ownerUserID, projectID, appInstanceID, installationGrantRevision)
	if err != nil {
		return notificationports.AppInstallationFacts{}, err
	}
	close(p.locked)
	select {
	case <-ctx.Done():
		return notificationports.AppInstallationFacts{}, ctx.Err()
	case <-p.release:
		return facts, nil
	}
}

// seedAppIngestInstallation inserts one active installation carrying the
// notifications.create grant the way a consented install would.
func seedAppIngestInstallation(t *testing.T, pool *pgxpool.Pool, owner, projectID, appID string, grants []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	installationID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.project_app_installations (
			id, owner_user_id, project_id, app_id, version, manifest_digest,
			granted_permissions, grant_revision, installed_at
		 ) VALUES ($1, $2, $3, $4, '1.0.0', $5, $6, 1, now())`,
		installationID, owner, projectID, appID,
		fakeDigest("manifest-"+installationID),
		pqTextArray(grants)); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	return installationID
}

func pqTextArray(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, strconv.Quote(value))
	}
	return "{" + strings.Join(escaped, ",") + "}"
}

// TestAppNotificationIngest proves the Core-private ingest authority over
// real PostgreSQL: the grant/epoch gate, exactly-once idempotent replay,
// stable conflicts, distinct-key multiplicity, the atomic burst quota, and
// zero side effects on any failure.
func TestAppNotificationIngest(t *testing.T) {
	service, _, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, 'app-ingest-project', 'App Ingest', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	installationID := seedAppIngestInstallation(t, pool, owner, projectID, "fixture-app",
		[]string{"knowledge.read", "notifications.create"})

	projectService, err := projectapp.NewInstallationService(projectpostgres.New(pool), stubAppCatalog{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	appAgent, err := orchestration.NewAppAgentService(projectService, stubAppTaskGateway{})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := orchestration.NewAppNotificationAuthorizer(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.WithAppAuthorizer(authorizer); err != nil {
		t.Fatal(err)
	}

	create := func(instance, key, title, body string, revision int64) (notificationapp.CreateAppNotificationResult, error) {
		return service.CreateAppNotification(ctx, notificationapp.CreateAppNotificationInput{
			OwnerUserID: owner, ProjectID: projectID, AppInstanceID: instance,
			InstallationGrantRevision: revision,
			IdempotencyKey:            key, Title: title, Body: body,
		})
	}

	// Fresh create: one app-origin notification bound to this installation.
	concurrent := make([]notificationapp.CreateAppNotificationResult, 8)
	concurrentErrs := make([]error, len(concurrent))
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			concurrent[slot], concurrentErrs[slot] = create(
				installationID, "key-1", "Hello from app", "Inert <script>text</script>", 1)
		}(i)
	}
	wg.Wait()
	first := concurrent[0]
	for i, err := range concurrentErrs {
		if err != nil {
			t.Fatalf("concurrent create %d: %v", i, err)
		}
		if concurrent[i].Notification.ID != first.Notification.ID ||
			concurrent[i].ChangeSequence != first.ChangeSequence {
			t.Fatalf("concurrent create %d drifted: %+v vs %+v", i, concurrent[i], first)
		}
	}
	if first.Notification.Kind != "app.instance.message" || first.Notification.Origin != "app" ||
		first.Notification.TargetID != installationID || first.Notification.AppID != "fixture-app" {
		t.Fatalf("unexpected projection: %+v", first.Notification)
	}
	if first.Notification.TargetKind != "app" || first.UnreadCount != 1 {
		t.Fatalf("unexpected target/unread: %+v", first)
	}
	if has := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE id = $1`, first.Notification.ID); has != 1 {
		t.Fatalf("notification row missing: %d", has)
	}

	// Same key/same request replays the exact first response.
	replay, err := create(installationID, "key-1", "Hello from app", "Inert <script>text</script>", 1)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Notification.ID != first.Notification.ID || replay.ChangeSequence != first.ChangeSequence {
		t.Fatalf("replay drifted: %+v vs %+v", replay, first)
	}
	// Same key/different request is a stable conflict.
	if _, err := create(installationID, "key-1", "Different", "body", 1); !errors.Is(err, notificationapp.ErrConflict) {
		t.Fatalf("conflict error = %v, want ErrConflict", err)
	}
	// A different key is a distinct intent: a second notification.
	second, err := create(installationID, "key-2", "Hello from app", "Inert <script>text</script>", 1)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if second.Notification.ID == first.Notification.ID {
		t.Fatal("distinct keys must produce distinct notifications")
	}

	// Stale grant epoch: denied with zero side effects.
	before := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE owner_user_id = $1`, owner)
	if _, err := create(installationID, "key-stale", "Stale", "body", 99); !errors.Is(err, notificationports.ErrAppNotificationDenied) {
		t.Fatalf("stale epoch error = %v, want ErrAppNotificationDenied", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notification_app_requests WHERE owner_user_id = $1 AND idempotency_key = 'key-stale'`, owner); got != 0 {
		t.Fatalf("denied create consumed its key: %d", got)
	}

	// Uninstalled installation: denied, zero side effects.
	if _, err := pool.Exec(ctx,
		`UPDATE workos_core.project_app_installations SET uninstalled_at = now() WHERE id = $1`, installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := create(installationID, "key-uninstalled", "Ghost", "body", 1); !errors.Is(err, notificationports.ErrAppNotificationDenied) {
		t.Fatalf("uninstalled error = %v, want ErrAppNotificationDenied", err)
	}
	after := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE owner_user_id = $1`, owner)
	if after != before {
		t.Fatalf("denied creates changed the notification set: %d -> %d", before, after)
	}

	// Every call re-authorizes before replay, so uninstalling immediately
	// revokes even a previously consumed key without changing old facts.
	if _, err := create(installationID, "key-1", "Hello from app", "Inert <script>text</script>", 1); !errors.Is(err, notificationports.ErrAppNotificationDenied) {
		t.Fatalf("uninstalled replay error = %v, want ErrAppNotificationDenied", err)
	}

	// Quota: the burst cap is atomic and per installation. Concurrent
	// distinct keys also exercise first-bucket creation arbitration.
	secondInstallation := seedAppIngestInstallation(t, pool, owner, projectID, "burst-app",
		[]string{"knowledge.read", "notifications.create"})
	burstErrs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, burstErrs[slot] = create(secondInstallation, fmt.Sprintf("burst-%d", slot), "Burst", "body", 1)
		}(i)
	}
	wg.Wait()
	for i, err := range burstErrs {
		if err != nil {
			t.Fatalf("burst create %d: %v", i, err)
		}
	}
	if _, err := create(secondInstallation, "burst-overflow", "Overflow", "body", 1); !errors.Is(err, notificationapp.ErrAppExhausted) {
		t.Fatalf("burst overflow error = %v, want ErrAppExhausted", err)
	}
	// The exhausted attempt never consumed its key: the mapping is absent.
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notification_app_requests WHERE owner_user_id = $1 AND idempotency_key = 'burst-overflow'`, owner); got != 0 {
		t.Fatalf("exhausted create consumed its key: %d", got)
	}
	// The exhausted attempt on the second installation never consumed its
	// key; a fresh key still works there after the earlier facts.
	if _, err := create(secondInstallation, "burst-overflow-retry", "Overflow", "body", 1); !errors.Is(err, notificationapp.ErrAppExhausted) {
		t.Fatalf("burst window must still bound retries: %v", err)
	}
}

// TestAppNotificationIngestRequiresGrant proves an installation whose grant
// lacks notifications.create is denied with zero side effects.
func TestAppNotificationIngestRequiresGrant(t *testing.T) {
	service, _, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, 'app-ingest-no-grant', 'App Ingest No Grant', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	installationID := seedAppIngestInstallation(t, pool, owner, projectID, "fixture-app", []string{"knowledge.read"})

	projectService, err := projectapp.NewInstallationService(projectpostgres.New(pool), stubAppCatalog{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	appAgent, err := orchestration.NewAppAgentService(projectService, stubAppTaskGateway{})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := orchestration.NewAppNotificationAuthorizer(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.WithAppAuthorizer(authorizer); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAppNotification(ctx, notificationapp.CreateAppNotificationInput{
		OwnerUserID: owner, ProjectID: projectID, AppInstanceID: installationID,
		InstallationGrantRevision: 1, IdempotencyKey: "no-grant-key",
		Title: "Denied", Body: "body",
	}); !errors.Is(err, notificationports.ErrAppNotificationDenied) {
		t.Fatalf("missing grant error = %v, want ErrAppNotificationDenied", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE owner_user_id = $1`, owner); got != 0 {
		t.Fatalf("denied create projected a notification: %d", got)
	}
}

// TestAppNotificationIngestAuthorizationSerializesRevocation proves the current
// authorization verdict and its notification write are one serializable
// decision: an uninstall that arrives after authorization waits for the
// notification commit, and every later create is denied.
func TestAppNotificationIngestAuthorizationSerializesRevocation(t *testing.T) {
	service, _, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, 'app-ingest-revocation-race', 'App Ingest Revocation Race', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	installationID := seedAppIngestInstallation(t, pool, owner, projectID, "race-app",
		[]string{"notifications.create"})

	projectService, err := projectapp.NewInstallationService(projectpostgres.New(pool), stubAppCatalog{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	appAgent, err := orchestration.NewAppAgentService(projectService, stubAppTaskGateway{})
	if err != nil {
		t.Fatal(err)
	}
	realAuthorizer, err := orchestration.NewAppNotificationAuthorizer(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	pausingAuthorizer := &pausingAppNotificationAuthorizer{
		inner: realAuthorizer, locked: make(chan struct{}), release: make(chan struct{}),
	}
	if err := service.WithAppAuthorizer(pausingAuthorizer); err != nil {
		t.Fatal(err)
	}

	createDone := make(chan error, 1)
	go func() {
		_, err := service.CreateAppNotification(ctx, notificationapp.CreateAppNotificationInput{
			OwnerUserID: owner, ProjectID: projectID, AppInstanceID: installationID,
			InstallationGrantRevision: 1, IdempotencyKey: "revocation-race-create",
			Title: "Authorized before uninstall", Body: "body",
		})
		createDone <- err
	}()
	select {
	case <-pausingAuthorizer.locked:
	case <-ctx.Done():
		t.Fatal("notification authorization did not acquire its locks")
	}

	updateStarted := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		close(updateStarted)
		_, err := pool.Exec(ctx,
			`UPDATE workos_core.project_app_installations SET uninstalled_at = now() WHERE id = $1`,
			installationID)
		updateDone <- err
	}()
	<-updateStarted
	select {
	case err := <-updateDone:
		t.Fatalf("uninstall bypassed notification authorization lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(pausingAuthorizer.release)
	select {
	case err := <-createDone:
		if err != nil {
			t.Fatalf("authorized notification create: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("authorized notification create did not complete")
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("uninstall after notification commit: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("uninstall did not resume after notification commit")
	}
	if got := countScratchRows(t, pool,
		`SELECT count(*) FROM workos_core.notifications WHERE owner_user_id = $1 AND app_installation_id = $2`,
		owner, installationID); got != 1 {
		t.Fatalf("authorized transaction projected %d notifications, want 1", got)
	}
}
