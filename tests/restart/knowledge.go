package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// indexSeed creates one project whose fake-harness review artifact must land
// in the durable knowledge projection, polls the authoritative Core read and
// then the Gateway-routed lexical search until the hit surfaces, and prints
// "project artifact digest" for the restart verify step.
func indexSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	index := indexv1connect.NewIndexServiceClient(client, baseURL)

	stamp := time.Now().UnixNano()
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-knowledge-%d", stamp), Name: "Restart Knowledge",
	}))
	if err != nil {
		return fmt.Errorf("create knowledge project: %w", err)
	}
	project := created.Msg.GetProject()
	if _, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "fake"},
	})); err != nil {
		return fmt.Errorf("bind fake provider: %w", err)
	}
	if _, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: fmt.Sprintf("restart-knowledge-task-%d", stamp),
		Input: &agentv1.AgentTaskInput{
			TargetScope:         &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:                "general",
			Goal:                "restart persistence knowledge review",
			OutputArtifactTypes: []string{"document.markdown.v1"},
		},
	})); err != nil {
		return fmt.Errorf("submit knowledge task: %w", err)
	}

	var artifactID, digest string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && artifactID == "" {
		listed, err := artifacts.ListArtifacts(ctx, connect.NewRequest(&artifactv1.ListArtifactsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10},
		}))
		if err == nil && len(listed.Msg.GetArtifacts()) > 0 {
			artifactID = listed.Msg.GetArtifacts()[0].GetId()
			digest = listed.Msg.GetArtifacts()[0].GetDigest()
		} else {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if artifactID == "" {
		return errors.New("review artifact never materialized")
	}

	// Bounded polling until the durable projection has consumed the
	// publication and the owner search surfaces the exact hit.
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		found, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
			ProjectId: project.GetId(), Query: "deterministic synthetic output",
			Page: &commonv1.PageRequest{PageSize: 50},
		}))
		if err == nil {
			for _, hit := range found.Msg.GetHits() {
				if hit.GetArtifactId() == artifactID && hit.GetDigest() == digest {
					fmt.Printf("%s %s %s\n", project.GetId(), artifactID, digest)
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("indexer never surfaced the seeded artifact")
}

// indexVerify re-runs the same owner search after a full Core + indexer
// restart: the committed document is still searchable, the exact ref and
// digest are unchanged, and the replayed publication created no duplicate.
func indexVerify(ctx context.Context, client *http.Client, baseURL, projectID, artifactID, digest string) error {
	index := indexv1connect.NewIndexServiceClient(client, baseURL)
	found, err := index.Search(ctx, connect.NewRequest(&indexv1.SearchRequest{
		ProjectId: projectID, Query: "deterministic synthetic output",
		Page: &commonv1.PageRequest{PageSize: 50},
	}))
	if err != nil {
		return fmt.Errorf("search after restart: %w", err)
	}
	matches := 0
	for _, hit := range found.Msg.GetHits() {
		if hit.GetArtifactId() == artifactID {
			if hit.GetDigest() != digest {
				return errors.New("indexed digest drifted across restart")
			}
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("artifact hit count after restart = %d, want exactly 1", matches)
	}
	fmt.Println("knowledge persistence verified for artifact", artifactID)
	return nil
}

var _ = strings.TrimSpace
var _ = os.Exit
