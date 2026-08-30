//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// newContextResolver builds the lease-bound resolver over an existing review
// fixture's pools and repositories.
func newContextResolver(f *reviewFixture) *orchestration.TaskContextResolver {
	resolver, err := orchestration.NewTaskContextResolver(f.pool, f.agentRepo, f.artifactRepo, ids.UUIDv7{})
	if err != nil {
		panic(err)
	}
	return resolver
}

// seedContextTask creates a second queued task in the fixture's project with
// the given refs pinned in its input, claims it, and returns (taskID, leaseID).
func seedContextTask(t *testing.T, f *reviewFixture, refsJSON string) (string, string) {
	t.Helper()
	ctx := context.Background()
	taskID := ids.UUIDv7{}.New()
	leaseID := ids.UUIDv7{}.New()
	payload := fmt.Sprintf(
		`{"targetScope":{"projectId":%q},"goal":"review the pinned context","contextRefs":[%s]}`,
		f.project, refsJSON)
	_, err := f.agentRepo.Create(ctx, agentdomain.Task{
		ID: taskID, OwnerUserID: f.owner, ProjectID: f.project,
		Input: []byte(payload), State: agentdomain.StateQueued, ProviderID: "fake",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, "context-idempotency-"+taskID)
	if err != nil {
		t.Fatalf("seed context task: %v", err)
	}
	lease, err := f.agentRepo.Claim(ctx, f.worker, 30*time.Second, leaseID, time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim context task: %v %v", lease, err)
	}
	return taskID, leaseID
}

func mustArtifactRef(t *testing.T, f *reviewFixture) string {
	t.Helper()
	artifactID := f.firstArtifactID(t)
	fact, content, err := f.artifactRepo.GetReviewContent(context.Background(), f.owner, artifactID)
	if err != nil {
		t.Fatalf("read materialized artifact: %v", err)
	}
	if fact.Digest != content.Digest {
		t.Fatalf("fixture digest drift: %q vs %q", fact.Digest, content.Digest)
	}
	return fmt.Sprintf(`{"type":"artifact.review.v1","id":%q,"revision":%q}`, artifactID, fact.Digest)
}

func (f *reviewFixture) firstArtifactID(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id FROM workos_core.project_review_artifacts WHERE source_task_id = $1 ORDER BY id LIMIT 1`,
		f.task).Scan(&id); err != nil {
		t.Fatalf("read artifact id: %v", err)
	}
	return id
}

// TestResolveTaskContextHappyPath proves the lease-bound resolver returns the
// exact canonical documents in request order, byte-identical on replay, for a
// task whose input pins a materialized review artifact.
func TestResolveTaskContextHappyPath(t *testing.T) {
	f := newReviewFixture(t)
	materializeBothReviewOutputs(t, f)
	resolver := newContextResolver(f)

	ref := mustArtifactRef(t, f)
	taskID, leaseID := seedContextTask(t, f, ref)

	documents, err := resolver.Resolve(context.Background(), leaseID, f.worker)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("unexpected document count: %d", len(documents))
	}
	document := documents[0]
	if document.RefType != "artifact.review.v1" || document.ArtifactID == "" ||
		document.Digest == "" || document.Title == "" || len(document.Content) == 0 {
		t.Fatalf("incomplete document: %#v", redactDocument(document))
	}
	replay, err := resolver.Resolve(context.Background(), leaseID, f.worker)
	if err != nil || len(replay) != 1 || !bytes.Equal(replay[0].Content, document.Content) ||
		replay[0].Digest != document.Digest {
		t.Fatalf("replay was not byte-identical: %v", err)
	}
	// A foreign worker is refused.
	if _, err := resolver.Resolve(context.Background(), leaseID, "someone-else"); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("foreign worker resolve was accepted: %v", err)
	}
	_ = taskID
}

// TestResolveTaskContextRejectsBadPins proves digest mismatch, foreign
// artifact, and web-bundle refs fail closed with the fixed verdicts.
func TestResolveTaskContextRejectsBadPins(t *testing.T) {
	f := newReviewFixture(t)
	materializeBothReviewOutputs(t, f)
	resolver := newContextResolver(f)

	artifactID := f.firstArtifactID(t)
	// Digest mismatch: the pinned digest is a well-formed digest of different
	// content.
	wrongDigest := artifactdomain.ReviewContentDigest("document.markdown.v1", []byte("different content"))
	badPins := []struct {
		name string
		ref  string
	}{
		{"digest mismatch", fmt.Sprintf(`{"type":"artifact.review.v1","id":%q,"revision":%q}`, artifactID, wrongDigest)},
		{"foreign artifact", fmt.Sprintf(`{"type":"artifact.review.v1","id":%q,"revision":%q}`, newUUIDForTest(920), mustKnownDigest(t, f))},
		{"wrong type", fmt.Sprintf(`{"type":"workspace.file.v1","id":%q,"revision":%q}`, artifactID, mustKnownDigest(t, f))},
	}
	for _, pin := range badPins {
		t.Run(pin.name, func(t *testing.T) {
			_, leaseID := seedContextTask(t, f, pin.ref)
			if _, err := resolver.Resolve(context.Background(), leaseID, f.worker); !errors.Is(err, agentdomain.ErrLeaseLost) && !errors.Is(err, agentdomain.ErrInvalid) {
				t.Fatalf("bad pin was accepted: %v", err)
			}
		})
	}
}

func mustKnownDigest(t *testing.T, f *reviewFixture) string {
	t.Helper()
	artifactID := f.firstArtifactID(t)
	_, content, err := f.artifactRepo.GetReviewContent(context.Background(), f.owner, artifactID)
	if err != nil {
		t.Fatalf("read digest: %v", err)
	}
	return content.Digest
}

func redactDocument(document orchestration.ResolvedDocument) map[string]any {
	return map[string]any{
		"refType": document.RefType, "artifactType": document.ArtifactType,
		"digest": document.Digest, "title": document.Title,
		"contentBytes": len(document.Content), "mediaType": document.MediaType,
	}
}

// materializeBothReviewOutputs drives the materializer once so the fixture's
// task owns both canonical review artifacts.
func materializeBothReviewOutputs(t *testing.T, f *reviewFixture) {
	t.Helper()
	ctx := context.Background()
	markdown := []byte("# Context fixture\n\nsynthetic markdown body\n")
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "document", "Context Fixture Doc", "document.markdown.v1", markdown); err != nil {
		t.Fatalf("materialize markdown: %v", err)
	}
	diff := []byte("diff --git a/x.ts b/x.ts\n--- a/x.ts\n+++ b/x.ts\n@@ -1 +1 @@\n-a\n+b\n")
	if _, _, err := f.materializer.MaterializeTaskArtifact(ctx, f.leaseID, f.worker, "patch", "Context Fixture Patch", "code.unified-diff.v1", diff); err != nil {
		t.Fatalf("materialize diff: %v", err)
	}
}
