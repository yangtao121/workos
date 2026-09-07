//go:build integration

package integration_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	feedpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	feedapp "github.com/yangtao121/workos/internal/core/indexfeed/application"
	feedtransport "github.com/yangtao121/workos/internal/core/indexfeed/transport"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	"github.com/yangtao121/workos/internal/indexer/adapters/coreclient"
)

// The rebuild fake feed has its own pagination; this gate exercises Core's
// actual repository, authority, Connect transport and Indexer client instead.
func TestIndexFeedCompletePagination(t *testing.T) {
	f := newRebuildFixture(t)
	ctx := context.Background()
	artifacts, err := artifactapp.New(artifactpostgres.New(f.pool), f.ids)
	if err != nil {
		t.Fatal(err)
	}
	projects := projectapp.New(projectpostgres.New(f.pool), f.ids)
	authority, err := orchestration.NewIndexSourceAuthority(artifacts, projects, projectpostgres.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	service, err := feedapp.NewService(feedpostgres.New(f.pool), authority, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	_, handler := feedtransport.NewConnectHandler(service)
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := coreclient.NewFeedClient(server.URL, f.owner, f.ids.New(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var wanted int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.project_review_artifacts a JOIN workos_core.projects p ON p.id=a.project_id WHERE p.archived_at IS NULL`).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	if wanted <= 200 {
		t.Fatal("fixture must span more than two pages")
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages >= 10 {
			t.Fatal("source cursor did not terminate")
		}
		rows, next, _, err := client.ReconcileSources(ctx, 100, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 100 {
			t.Fatal("source page exceeded bound")
		}
		for _, row := range rows {
			if seen[row.ArtifactID] {
				t.Fatal("duplicate source across pages")
			}
			seen[row.ArtifactID] = true
		}
		if next == "" {
			break
		}
		if next == cursor {
			t.Fatal("source cursor did not advance")
		}
		cursor = next
	}
	if len(seen) != wanted {
		t.Fatalf("Core paginated %d/%d review sources", len(seen), wanted)
	}
	// Tied archived_at timestamps exercise the ID portion of the keyset cursor.
	execScratch(t, f.pool, `INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at, archived_at)
 SELECT id, $1, 'archived-page-' || n, 'Archived page fixture', id, id, now(), now(), now()
 FROM (SELECT n, ('01999999-5678-7abc-8abc-' || lpad(to_hex(n),12,'0'))::uuid AS id FROM generate_series(1,201) n) s`, f.owner)
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM workos_core.projects WHERE archived_at IS NOT NULL`).Scan(&wanted); err != nil {
		t.Fatal(err)
	}
	seen, cursor = map[string]bool{}, ""
	for pages := 0; ; pages++ {
		if pages >= 10 {
			t.Fatal("archive cursor did not terminate")
		}
		rows, next, err := client.ReconcileArchivedProjects(ctx, 100, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 100 {
			t.Fatal("archive page exceeded bound")
		}
		for _, row := range rows {
			if seen[row.ProjectID] {
				t.Fatal("duplicate archived project across pages")
			}
			seen[row.ProjectID] = true
		}
		if next == "" {
			break
		}
		if next == cursor {
			t.Fatal("archive cursor did not advance")
		}
		cursor = next
	}
	if len(seen) != wanted {
		t.Fatalf("Core paginated %d/%d archived projects", len(seen), wanted)
	}
}
