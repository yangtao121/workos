//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/indexer/application"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/tests/fixtures/embedding"
)

type countingWorkspaceModel struct {
	embedding.Model
	documents int
}

func (m *countingWorkspaceModel) Document(ctx context.Context, text string) ([]float32, error) {
	m.documents++
	return m.Model.Document(ctx, text)
}

func TestWorkspaceReusesOnlyMatchingModelVectors(t *testing.T) {
	pool, original, owner, project := modelFixture(t)
	ctx := context.Background()
	model := &countingWorkspaceModel{}
	projection, err := application.NewModelProjection(original.ModelProjectionStore, model)
	if err != nil {
		t.Fatal(err)
	}
	generator := ids.UUIDv7{}
	source, err := projection.InsertWorkspaceSource(ctx, ports.WorkspaceSource{ID: generator.New(), OwnerUserID: owner, ProjectID: project, RootPath: "/fixture/vector-cache"})
	if err != nil {
		t.Fatal(err)
	}
	file := func(name, content string) ports.MountFile {
		return ports.MountFile{SourceID: generator.New(), RelPath: name, Title: name, Content: []byte(content), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content)))}
	}
	files := []ports.MountFile{file("stable.md", "stable content"), file("changed.md", "original content")}
	sync := func(wantCalls int) {
		t.Helper()
		updated, _, _, err := projection.ConvergeWorkspacePass(ctx, source, files, 0, generator.New, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		source = updated
		if model.documents != wantCalls {
			t.Fatalf("inference calls=%d, want=%d", model.documents, wantCalls)
		}
	}
	sync(2)
	sync(2)
	changed := file("changed.md", "new content")
	changed.SourceID = files[1].SourceID
	files[1] = changed
	files = append(files, file("added.md", "added content"))
	sync(4)
	files[0].Title = "Changed label"
	sync(5)
	// A different recipe's cached vector cannot be reused even for identical text.
	if _, err := pool.Exec(ctx, `UPDATE workos_index.documents SET embedding_model='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE source_type='workspace.file.v1'`); err != nil {
		t.Fatal(err)
	}
	sync(8)
	sync(8)
}
