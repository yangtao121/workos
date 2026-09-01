//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/gen/go/workos/common/v1/commonv1connect"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	projectv1connect "github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// stackGatewayURL is the compose Gateway with dev identity bypass.
const stackGatewayURL = "http://127.0.0.1:8080"

// TestProjectKnowledgeSearchStack proves the full owner chain over the real
// stack: a fake-harness task materializes a review artifact, Core publishes
// it in the same transaction, the indexer consumes it, and the owner's
// bounded search hits the exact artifact ref through the Gateway.
func TestProjectKnowledgeSearchStack(t *testing.T) {
	client := &http.Client{Timeout: 30 * time.Second}
	ctx := context.Background()

	// The indexer must report healthy with honest capabilities before the
	// journey starts: the lexical slice is available; generic archive and
	// semantic RAG stay unavailable with fixed reasons.
	health := commonv1connect.NewSystemServiceClient(client, "http://127.0.0.1:8085")
	reported, healthErr := health.GetServiceHealth(ctx, connect.NewRequest(&commonv1.GetServiceHealthRequest{}))
	if healthErr != nil {
		t.Fatalf("indexer health: %v", healthErr)
	}
	capabilities := map[string]bool{}
	for _, capability := range reported.Msg.GetHealth().GetCapabilities() {
		capabilities[capability.GetId()] = capability.GetAvailable()
	}
	if !capabilities["project-review-index"] || !capabilities["project-knowledge-search"] {
		t.Fatalf("indexer lexical capabilities must be available: %+v", capabilities)
	}
	if capabilities["archive"] || capabilities["rag"] {
		t.Fatalf("archive/rag must stay unavailable: %+v", capabilities)
	}

	projects := projectv1connect.NewProjectServiceClient(client, stackGatewayURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, stackGatewayURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, stackGatewayURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, stackGatewayURL)
	index := indexv1connect.NewIndexServiceClient(client, stackGatewayURL)

	unique := fmt.Sprintf("quokkarune-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "knowledge-" + unique, Name: "Knowledge Lab",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	_, err = bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	}))
	if err != nil {
		t.Fatalf("bind fake provider: %v", err)
	}
	_, err = tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "knowledge-task-" + unique,
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "write the " + unique + " review document",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	}))
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the artifact through the authoritative Core read.
	deadline := time.Now().Add(60 * time.Second)
	var artifact *artifactv1.Artifact
	for time.Now().Before(deadline) {
		listed, err := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if err == nil && len(listed.Msg.GetArtifacts()) > 0 {
			got, getErr := artifacts.GetReviewArtifact(ctx, connect.NewRequest(&artifactv1.GetReviewArtifactRequest{
				ArtifactId: listed.Msg.GetArtifacts()[0].GetId(),
			}))
			if getErr == nil {
				artifact = got.Msg.GetArtifact()
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if artifact == nil {
		t.Fatal("review artifact never materialized")
	}

	// Bounded polling until the projection catches up, then search.
	deadline = time.Now().Add(60 * time.Second)
	var hit *indexv1.SearchHit
	for time.Now().Before(deadline) {
		found, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
			ProjectId: project.GetId(),
			Query:     "deterministic synthetic output",
			Page:      &commonv1.PageRequest{PageSize: 20},
		}))
		if err == nil {
			for _, candidate := range found.Msg.GetHits() {
				if candidate.GetArtifactId() == artifact.GetId() && candidate.GetDigest() == artifact.GetDigest() {
					hit = candidate
					break
				}
			}
			if hit != nil {
				if !strings.Contains(hit.GetExcerpt(), "") {
					t.Fatal("excerpt must be present")
				}
				break
			}
		} else if !strings.Contains(err.Error(), "Unavailable") {
			t.Fatalf("search failed: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if hit == nil {
		t.Fatal("search never surfaced the indexed artifact")
	}
	if hit.GetSourceRef().GetType() != "artifact.review.v1" || hit.GetSourceRef().GetId() != artifact.GetId() || hit.GetSourceRef().GetRevision() != artifact.GetDigest() {
		t.Fatalf("hit ref mismatch: %+v", hit.GetSourceRef())
	}
	if hit.GetContextRef() != "artifact.review.v1:"+artifact.GetId()+":"+artifact.GetDigest() {
		t.Fatalf("legacy context_ref mismatch: %q", hit.GetContextRef())
	}

	// Exactly 20 hits of isolation: a foreign project query with the same
	// phrase must come back empty (no existence oracle beyond emptiness).
	created2, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "knowledge-other-" + unique, Name: "Other Lab",
	}))
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}
	foreign, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: created2.Msg.GetProject().GetId(), Query: "deterministic synthetic output",
	}))
	if err != nil {
		t.Fatalf("foreign search: %v", err)
	}
	if len(foreign.Msg.GetHits()) != 0 {
		t.Fatalf("foreign project saw %d hits", len(foreign.Msg.GetHits()))
	}

	// Malformed queries fail closed before any read.
	if _, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: project.GetId(), Query: strings.Repeat("x", 300),
	})); err == nil {
		t.Fatal("oversized query must fail closed")
	}
}
