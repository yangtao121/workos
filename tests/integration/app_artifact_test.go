//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	indexpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	indexdomain "github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type failingAppArtifactFeed struct{}

func (failingAppArtifactFeed) AppendReviewArtifactUpsert(context.Context, dbtx.Tx, indexdomain.Publication) error {
	return errors.New("fixture unavailable")
}

func TestAppArtifacts(t *testing.T) {
	_, notifications, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1,$2,'app-artifact','Artifact fixture',$3,$4,now(),now())`, projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatal(err)
	}
	installationID := seedAppIngestInstallation(t, pool, owner, projectID, "artifact-fixture", []string{"artifact.read", "artifact.write"})
	installations, err := projectapp.NewInstallationService(projectpostgres.New(pool), stubAppCatalog{}, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := orchestration.NewAppAgentService(installations, stubAppTaskGateway{})
	if err != nil {
		t.Fatal(err)
	}
	newService := func(feed orchestration.IndexPublicationSink) *orchestration.AppArtifactService {
		service, err := orchestration.NewAppArtifactService(pool, auth, artifactpostgres.New(pool), feed, notifications, ids.UUIDv7{})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	feed := indexpostgres.New(pool)
	service := newService(feed)
	scope := orchestration.AppArtifactScope{OwnerUserID: owner, ProjectID: projectID, AppInstanceID: installationID, GrantRevision: 1}
	create := func(s *orchestration.AppArtifactService, scope orchestration.AppArtifactScope, key, content string) (artifactdomain.ReviewArtifact, error) {
		return s.Create(ctx, scope, key, artifactdomain.TypeMarkdown, "App document", []byte(content))
	}
	results := make([]artifactdomain.ReviewArtifact, 8)
	errs := make([]error, len(results))
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			results[i], errs[i] = create(service, scope, "document", "# Fixture\n")
		}(i)
	}
	group.Wait()
	for i, a := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if a.ID != results[0].ID || a.SourceTask != "" || a.SourceAppInstanceID != installationID {
			t.Fatal("concurrent replay/provenance drift")
		}
	}
	replay, err := create(newService(feed), scope, "document", "# Fixture\r\n")
	if err != nil || replay.ID != results[0].ID {
		t.Fatalf("restart canonical replay: %v", err)
	}
	if _, err := create(service, scope, "document", "different"); !errors.Is(err, artifactdomain.ErrIdempotencyConflict) {
		t.Fatalf("conflict: %v", err)
	}
	repository := artifactpostgres.New(pool)
	metadata, err := repository.Get(ctx, owner, replay.ID)
	if err != nil || metadata.SourceAppInstanceID != installationID || metadata.SourceTaskID != "" {
		t.Fatalf("metadata: %v", err)
	}
	fact, content, err := repository.GetReviewContent(ctx, owner, replay.ID)
	if err != nil || fact.ID != replay.ID || string(content.Content) != "# Fixture\n" {
		t.Fatalf("content: %v", err)
	}
	if _, err := service.Open(ctx, scope, replay.ID); err != nil {
		t.Fatal(err)
	}
	otherScope := scope
	otherScope.ProjectID = ids.UUIDv7{}.New()
	if _, err := service.Open(ctx, otherScope, replay.ID); err == nil {
		t.Fatal("foreign project accepted")
	}
	secondProject := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1,$2,'app-artifact-other','Other project',$3,$4,now(),now())`, secondProject, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatal(err)
	}
	secondInstallation := seedAppIngestInstallation(t, pool, owner, secondProject, "artifact-fixture", []string{"artifact.read", "artifact.write"})
	otherScope = orchestration.AppArtifactScope{OwnerUserID: owner, ProjectID: secondProject, AppInstanceID: secondInstallation, GrantRevision: 1}
	if _, err := service.Open(ctx, otherScope, replay.ID); !errors.Is(err, artifactdomain.ErrNotFound) {
		t.Fatalf("authorized installation reading another project: %v", err)
	}
	otherScope = scope
	otherScope.GrantRevision++
	if _, err := create(service, otherScope, "stale", "stale"); !errors.Is(err, orchestration.ErrAppGrantStale) {
		t.Fatalf("stale epoch: %v", err)
	}
	if _, err := create(newService(failingAppArtifactFeed{}), scope, "rollback", "rollback"); err == nil {
		t.Fatal("feed failure ignored")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.project_review_artifacts WHERE source_app_instance_id=$1`, installationID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("atomic rollback count=%d: %v", count, err)
	}
	if _, err := create(service, scope, "rollback", "rollback"); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.notifications WHERE owner_user_id=$1`, owner).Scan(&count); err != nil || count != 2 {
		t.Fatalf("notification deduplication count=%d: %v", count, err)
	}
	for i := 2; i < 100; i++ {
		if _, err := create(service, scope, fmt.Sprintf("quota-%d", i), "Quota fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := create(service, scope, "quota-overflow", "Overflow"); !errors.Is(err, artifactdomain.ErrQuota) {
		t.Fatalf("quota not enforced: %v", err)
	}
	if _, err := create(service, scope, "document", "# Fixture\n"); err != nil {
		t.Fatalf("quota blocked a replay: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workos_core.project_app_installations SET grant_revision=grant_revision+1, granted_permissions='{}'::text[] WHERE id=$1`, installationID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Open(ctx, scope, replay.ID); err == nil {
		t.Fatal("revoked open accepted")
	}
	if _, err := create(service, scope, "after-revoke", "denied"); err == nil {
		t.Fatal("revoked create accepted")
	}
}
