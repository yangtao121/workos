//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	"github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/indexer/adapters/localmount"
	postgres "github.com/yangtao121/workos/internal/indexer/adapters/postgres"
	app "github.com/yangtao121/workos/internal/indexer/application"
	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/indexer/transport"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

type workspaceStopAdmin struct {
	transport.AdminService
	workspaces *app.WorkspaceIngestor
}

func (a workspaceStopAdmin) ListWorkspaceSources(ctx context.Context) ([]ports.WorkspaceSource, error) {
	return a.workspaces.List(ctx)
}
func (a workspaceStopAdmin) StopWorkspaceSource(ctx context.Context, sourceID, etag string) (ports.WorkspaceSource, error) {
	return a.workspaces.Stop(ctx, sourceID, etag)
}

func TestWorkspaceStop(t *testing.T) {
	ctx := context.Background()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	generator := ids.UUIDv7{}
	repo, err := postgres.New(pool, generator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureBootstrapGeneration(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	ingestor, err := app.NewWorkspaceIngestor(repo, localmount.NewWalker(), generator.New)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeWorkspaceFile(t, root, "notes.md", "withdrawn workspace fixture")
	source, err := ingestor.Register(ctx, generator.New(), generator.New(), root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ingestor.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	source = result.Source
	_, handler := transport.NewAdminConnectHandler(workspaceStopAdmin{workspaces: ingestor})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := indexv1connect.NewIndexAdminServiceClient(server.Client(), server.URL)
	listed, err := client.ListWorkspaceSources(ctx, connect.NewRequest(&indexv1.ListWorkspaceSourcesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.Sources) != 1 || listed.Msg.Sources[0].Etag == "" {
		t.Fatal("source etag missing from admin wire")
	}
	etag := listed.Msg.Sources[0].Etag
	stop := func(etag string) (*indexv1.StopWorkspaceSourceResponse, error) {
		reply, err := client.StopWorkspaceSource(ctx, connect.NewRequest(&indexv1.StopWorkspaceSourceRequest{SourceId: source.ID, ExpectedEtag: etag}))
		if err != nil {
			return nil, err
		}
		return reply.Msg, nil
	}
	search := app.NewSearchServiceForTest(repo)
	hits, err := search.Search(ctx, app.SearchInput{OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID, RawQuery: "withdrawn", PageSize: 10})
	if err != nil || len(hits.Page.Hits) != 1 {
		t.Fatalf("initial search: %v", err)
	}
	hit := hits.Page.Hits[0]
	ref := domain.DocumentRead{OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID, SourceID: hit.ArtifactID, SourceType: domain.SourceWorkspaceFile, Digest: hit.Digest}
	// A failed final status update must not withdraw the documents first.
	execScratch(t, pool, `CREATE FUNCTION workos_index.reject_workspace_stop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status = 'stopped' THEN RAISE EXCEPTION 'fixture stop failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_workspace_stop BEFORE UPDATE ON workos_index.workspace_sources FOR EACH ROW EXECUTE FUNCTION workos_index.reject_workspace_stop()`)
	if _, err := stop(etag); err == nil {
		t.Fatal("failed stop succeeded")
	}
	if _, err := search.ReadDocument(ctx, ref); err != nil {
		t.Fatalf("failed stop withdrew document: %v", err)
	}
	unchanged, err := repo.GetWorkspaceSource(ctx, source.ID)
	if err != nil || !unchanged.UpdatedAt.Equal(source.UpdatedAt) {
		t.Fatalf("failed stop changed source: %v", err)
	}
	execScratch(t, pool, `DROP TRIGGER reject_workspace_stop ON workos_index.workspace_sources; DROP FUNCTION workos_index.reject_workspace_stop()`)
	stopped, err := stop(etag)
	if err != nil || stopped.Source.Status != domain.WorkspaceStopped || stopped.Source.IndexedCount != 0 || stopped.Source.TombstonedCount != 1 {
		t.Fatalf("stop outcome: %v", err)
	}
	if _, err := search.ReadDocument(ctx, ref); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stopped snapshot remained readable: %v", err)
	}
	hits, err = search.Search(ctx, app.SearchInput{OwnerUserID: source.OwnerUserID, ProjectID: source.ProjectID, RawQuery: "withdrawn", PageSize: 10})
	if err != nil || len(hits.Page.Hits) != 0 {
		t.Fatalf("stopped source still searchable: %v", err)
	}
	if _, err := ingestor.Sync(ctx, source.ID); !errors.Is(err, app.ErrWorkspaceStopped) {
		t.Fatalf("stopped source synced: %v", err)
	}
	if _, _, _, err := repo.ConvergeWorkspacePass(ctx, source, nil, 0, generator.New, time.Now().UTC()); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("admitted old scan changed stopped binding: %v", err)
	}
	if _, err := stop(etag); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale etag: %v", err)
	}
	repeat, err := stop(stopped.Source.Etag)
	if err != nil || repeat.Source.Etag != stopped.Source.Etag {
		t.Fatalf("current stopped binding replay: %v", err)
	}
	// Rebinding grants a new version; an old stop must never withdraw it.
	if _, err := ingestor.Register(ctx, source.OwnerUserID, source.ProjectID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := stop(stopped.Source.Etag); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("old stop withdrew rebound source: %v", err)
	}
	if _, err := ingestor.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := search.ReadDocument(ctx, ref); err != nil {
		t.Fatalf("rebound snapshot unavailable: %v", err)
	}
}
