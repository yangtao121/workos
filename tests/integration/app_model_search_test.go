//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/application"
	indexports "github.com/yangtao121/workos/internal/indexer/ports"
	indextransport "github.com/yangtao121/workos/internal/indexer/transport"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/surface/adapters/indexerclient"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type appModelIndexSurface struct {
	indextransport.IndexService
	search *application.SearchService
}

func (s appModelIndexSurface) SearchHybrid(ctx context.Context, input application.SearchInput) (application.SearchResult, error) {
	return s.search.SearchHybrid(ctx, input)
}

func TestAppModelSearchFiltersWorkspaceBeforePagination(t *testing.T) {
	_, projection, owner, project := modelFixture(t)
	ctx := context.Background()
	generator := ids.UUIDv7{}
	source, err := projection.InsertWorkspaceSource(ctx, indexports.WorkspaceSource{ID: generator.New(), OwnerUserID: owner, ProjectID: project, RootPath: "/fixture/model-search"})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("checkpoint durable replay")
	_, _, _, err = projection.ConvergeWorkspacePass(ctx, source, []indexports.MountFile{{RelPath: "checkpoint.md", Title: "checkpoint", Content: content, Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)), SourceID: generator.New()}}, 0, generator.New, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	unfiltered, err := application.NewSearchServiceForTest(projection).SearchHybrid(ctx, application.SearchInput{OwnerUserID: owner, ProjectID: project, RawQuery: "checkpoint", PageSize: 1})
	if err != nil || len(unfiltered.Page.Hits) != 1 || unfiltered.Page.Hits[0].SourceType != "workspace.file.v1" {
		t.Fatalf("workspace fixture did not precede review: %+v %v", unfiltered, err)
	}
	_, handler := indextransport.NewConnectHandler(appModelIndexSurface{search: application.NewSearchServiceForTest(projection)})
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client, err := indexerclient.NewKnowledgeSearch(server.URL, generator.New())
	if err != nil {
		t.Fatal(err)
	}
	// The equally scored, newer workspace row sorts first without a source
	// filter. Filtering after pagination would lose the valid review result.
	page, err := client.Search(ctx, ports.KnowledgeSearchQuery{OwnerUserID: owner, ProjectID: project, Query: "checkpoint", PageSize: 1})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].ArtifactID != "01999999-9999-7999-8999-000000000a03" || page.NextPageToken != "" {
		t.Fatalf("workspace poisoned App model result: %+v %v", page, err)
	}
}
