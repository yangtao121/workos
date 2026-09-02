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
	if !capabilities["project-review-index"] || !capabilities["project-knowledge-search"] || !capabilities["project-knowledge-rebuild"] {
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
	bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
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
			OutputArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"},
		},
	}))
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}

	// Wait for the artifact through the authoritative Core read.
	deadline := time.Now().Add(60 * time.Second)
	var artifact *artifactv1.Artifact
	initialArtifacts := map[string]struct{}{}
	for time.Now().Before(deadline) {
		listed, err := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if err == nil && len(listed.Msg.GetArtifacts()) >= 2 {
			for _, candidate := range listed.Msg.GetArtifacts() {
				initialArtifacts[candidate.GetId()] = struct{}{}
			}
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
				if strings.TrimSpace(hit.GetExcerpt()) == "" ||
					!strings.Contains(strings.ToLower(hit.GetExcerpt()), "deterministic synthetic output") {
					t.Fatalf("excerpt does not contain the matched phrase: %q", hit.GetExcerpt())
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

	// Real-stack pagination is deterministic, tamper evident, bound to the
	// canonical query, and snapshots out later publications.
	deadline = time.Now().Add(60 * time.Second)
	var snapshotToken string
	for time.Now().Before(deadline) {
		firstPage, pageErr := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
			ProjectId: project.GetId(), Query: "fake harness",
			Page: &commonv1.PageRequest{PageSize: 1},
		}))
		if pageErr == nil && len(firstPage.Msg.GetHits()) == 1 && firstPage.Msg.GetPage().GetNextPageToken() != "" {
			snapshotToken = firstPage.Msg.GetPage().GetNextPageToken()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if snapshotToken == "" {
		t.Fatal("first page never exposed the second initial artifact")
	}
	if _, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: project.GetId(), Query: "different query",
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: snapshotToken},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cross-query page token must be InvalidArgument, got %v", err)
	}
	if _, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: project.GetId(), Query: "fake harness",
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: snapshotToken + "x"},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("tampered page token must be InvalidArgument, got %v", err)
	}

	_, err = tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "knowledge-later-task-" + unique,
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "write a later " + unique + " review document",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	}))
	if err != nil {
		t.Fatalf("submit later task: %v", err)
	}
	deadline = time.Now().Add(60 * time.Second)
	var laterArtifactID string
	for time.Now().Before(deadline) {
		listed, listErr := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if listErr == nil && len(listed.Msg.GetArtifacts()) >= 3 {
			for _, candidate := range listed.Msg.GetArtifacts() {
				if _, existed := initialArtifacts[candidate.GetId()]; !existed {
					laterArtifactID = candidate.GetId()
					break
				}
			}
			if laterArtifactID != "" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if laterArtifactID == "" {
		t.Fatal("later review artifact never materialized")
	}
	deadline = time.Now().Add(60 * time.Second)
	laterIndexed := false
	for time.Now().Before(deadline) {
		fresh, searchErr := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
			ProjectId: project.GetId(), Query: "fake harness",
			Page: &commonv1.PageRequest{PageSize: 20},
		}))
		if searchErr == nil && len(fresh.Msg.GetHits()) >= 3 {
			laterIndexed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !laterIndexed {
		t.Fatal("later review artifact never reached the fresh search snapshot")
	}
	secondPage, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: project.GetId(), Query: "fake harness",
		Page: &commonv1.PageRequest{PageSize: 1, PageToken: snapshotToken},
	}))
	if err != nil {
		t.Fatalf("second snapshot page: %v", err)
	}
	if len(secondPage.Msg.GetHits()) != 1 || secondPage.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("exact two-hit snapshot must end on page two: %+v", secondPage.Msg)
	}
	if secondPage.Msg.GetHits()[0].GetArtifactId() == laterArtifactID {
		t.Fatal("later publication leaked into an existing search snapshot")
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

	// Project archive is a durable Core publication. The indexer must remove
	// every hit without exposing a half-tombstoned projection.
	current, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
	if err != nil {
		t.Fatalf("get project before archive: %v", err)
	}
	if current.Msg.GetProject().GetRevision() < bound.Msg.GetProject().GetRevision() {
		t.Fatalf("project revision regressed before archive: %+v", current.Msg.GetProject())
	}
	if _, err := projects.ArchiveProject(ctx, connect.NewRequest(&projectv1.ArchiveProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: current.Msg.GetProject().GetRevision(),
	})); err != nil {
		t.Fatalf("archive project: %v", err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		archived, searchErr := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
			ProjectId: project.GetId(), Query: "deterministic synthetic output",
		}))
		if searchErr == nil && len(archived.Msg.GetHits()) == 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("archived project remained searchable")
}
