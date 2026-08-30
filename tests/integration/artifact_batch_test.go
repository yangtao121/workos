//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/yangtao121/workos/internal/core/orchestration"
)

// TestArtifactBatchAtomicity proves the batch protocol's all-or-none
// guarantee against real PostgreSQL: a batch whose second member is invalid
// leaves zero artifacts, zero mappings, and zero events; a valid batch
// materializes both in request order; an identical replay returns exactly
// the first facts without duplicating rows or events.
func TestArtifactBatchAtomicity(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	resolver := newContextResolver(f)
	_ = resolver

	newBatch := func(diffContent string) []orchestration.BatchOutput {
		return []orchestration.BatchOutput{
			{Key: "document", Title: "Doc", Type: "document.markdown.v1", Content: []byte("# batch doc\n")},
			{Key: "patch", Title: "Patch", Type: "code.unified-diff.v1", Content: []byte(diffContent)},
		}
	}

	// All-or-none: the second output violates the requested-type contract,
	// so the first must not survive either.
	badBatch := []orchestration.BatchOutput{
		{Key: "document", Title: "Doc", Type: "document.markdown.v1", Content: []byte("# doomed doc\n")},
		{Key: "patch", Title: "Patch", Type: "app.web-bundle.v1", Content: []byte("not a diff\n")},
	}
	if _, _, err := f.materializer.MaterializeTaskArtifactBatch(ctx, f.leaseID, f.worker, badBatch); err == nil {
		t.Fatal("invalid batch was accepted")
	}
	if n := f.artifactCount(t); n != 0 {
		t.Fatalf("artifacts leaked from a failed batch: %d", n)
	}
	if n := f.outputCount(t); n != 0 {
		t.Fatalf("mappings leaked from a failed batch: %d", n)
	}
	if n := f.eventCount(t, "artifact_created"); n != 0 {
		t.Fatalf("events leaked from a failed batch: %d", n)
	}

	// Valid batch: both outputs commit in request order with consecutive
	// Core-minted event sequences.
	goodBatch := newBatch("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")
	artifacts, events, err := f.materializer.MaterializeTaskArtifactBatch(ctx, f.leaseID, f.worker, goodBatch)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts: %d", len(artifacts))
	}
	if len(events) != 2 {
		t.Fatalf("events: %d", len(events))
	}
	if got := artifacts[0].GetType(); got != "document.markdown.v1" {
		t.Fatalf("first artifact type: %q", got)
	}
	if got := artifacts[1].GetType(); got != "code.unified-diff.v1" {
		t.Fatalf("second artifact type: %q", got)
	}
	firstSeq, secondSeq := events[0].GetSequence(), events[1].GetSequence()
	if secondSeq != firstSeq+1 {
		t.Fatalf("event sequences are not consecutive: %d then %d", firstSeq, secondSeq)
	}

	// Exact replay: identical inputs return the first facts; no new rows.
	replayArtifacts, replayEvents, err := f.materializer.MaterializeTaskArtifactBatch(ctx, f.leaseID, f.worker, goodBatch)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(replayArtifacts) != 2 {
		t.Fatalf("replay artifacts: %d", len(replayArtifacts))
	}
	if len(replayEvents) != 2 {
		t.Fatalf("replay events: %d", len(replayEvents))
	}
	if replayArtifacts[0].GetId() != artifacts[0].GetId() || replayArtifacts[1].GetId() != artifacts[1].GetId() {
		t.Fatalf("replay minted new artifacts: %q %q", replayArtifacts[0].GetId(), replayArtifacts[1].GetId())
	}
	if replayEvents[0].GetSequence() != firstSeq || replayEvents[1].GetSequence() != secondSeq {
		t.Fatalf("replay events drifted: %d/%d", replayEvents[0].GetSequence(), replayEvents[1].GetSequence())
	}
	if n := f.artifactCount(t); n != 2 {
		t.Fatalf("artifact count: %d", n)
	}
	if n := f.outputCount(t); n != 2 {
		t.Fatalf("output count: %d", n)
	}
	if n := f.eventCount(t, "artifact_created"); n != 2 {
		t.Fatalf("event count: %d", n)
	}

	// Different content for an already-consumed key is the stable conflict.
	conflict := []orchestration.BatchOutput{
		{Key: "document", Title: "Doc", Type: "document.markdown.v1", Content: []byte("# different doc\n")},
	}
	if _, _, err := f.materializer.MaterializeTaskArtifactBatch(ctx, f.leaseID, f.worker, conflict); err == nil {
		t.Fatal("conflicting batch was accepted")
	}

}

func TestSequentialDoubleMaterializeProbe(t *testing.T) {
	f := newReviewFixture(t)
	out1, _, err := f.materializer.MaterializeTaskArtifact(context.Background(), f.leaseID, f.worker, "document", "T", "document.markdown.v1", []byte("# v0\n"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	out2, _, err := f.materializer.MaterializeTaskArtifact(context.Background(), f.leaseID, f.worker, "document", "T", "document.markdown.v1", []byte("# v0\n"))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	var dbName string
	_ = f.pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&dbName)
	t.Logf("probe db=%s task=%s lease=%s", dbName, f.task, f.leaseID)
	var srcTask, outputKey string
	if err := f.pool.QueryRow(context.Background(), `SELECT source_task_id, output_key FROM workos_core.project_review_artifacts ORDER BY id LIMIT 1`).Scan(&srcTask, &outputKey); err != nil {
		t.Logf("scan artifact row: %v", err)
	} else {
		t.Logf("artifact row: source_task=%s output_key=%s expect task=%s", srcTask, outputKey, f.task)
	}
	t.Logf("first=%s second=%s same=%v artifacts=%d", out1.GetId(), out2.GetId(), out1.GetId() == out2.GetId(), f.artifactCount(t))
}
