package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	"github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	"github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
)

// bridgeSeed drives one full App Bridge chain before the restart: a granted
// web bundle app is installed, a surface with a bridge token is opened, and
// one project task runs through the real Task Router and Fake Harness. The
// bridge token, task id, and run key are printed for the verify step.
func bridgeSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-bridge-%d", stamp)

	created, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: fmt.Sprintf("restart-bridge-artifact-%d", stamp),
		Artifact:       &artifactv1.Artifact{Title: "Restart Bridge"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("<!doctype html><title>Restart Bridge</title><div id=\"root\"></div>")},
			},
		},
	}))
	if err != nil {
		return fmt.Errorf("create bridge artifact: %w", err)
	}
	artifact := created.Msg.GetArtifact()

	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Restart Bridge App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: %s
  artifactDigest: %s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
permissions: [agent.task.run, agent.event.watch]
resources: {}
health: {}
maintainer: {}
`, appID, artifact.GetId(), artifact.GetDigest())
	if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-bridge-register-%d", stamp), ManifestYaml: []byte(manifest),
	})); err != nil {
		return fmt.Errorf("register restart bridge app: %w", err)
	}
	projectResponse, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-bridge-project-%d", stamp), Name: "Restart Bridge",
	}))
	if err != nil {
		return fmt.Errorf("create restart bridge project: %w", err)
	}
	project := projectResponse.Msg.GetProject()
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("restart-bridge-install-%d", stamp),
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		return fmt.Errorf("install restart bridge app: %w", err)
	}
	opened, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    fmt.Sprintf("restart-bridge-surface-%d", stamp),
		AppInstanceId:     installed.Msg.GetInstallation().GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		return fmt.Errorf("open restart bridge surface: %w", err)
	}
	token := opened.Msg.GetSession().GetBridgeToken()
	if token == "" {
		return errors.New("restart bridge surface has no bridge token")
	}
	runKey := fmt.Sprintf("restart-bridge-run-%d", stamp)
	runRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey,
		Goal:           "Restart bridge goal",
	})
	runRequest.Header().Set("X-WorkOS-Bridge-Token", token)
	run, err := bridge.RunAgentTask(ctx, runRequest)
	if err != nil {
		return fmt.Errorf("bridge run failed: %w", err)
	}
	fmt.Printf("%s %s %s\n", token, run.Msg.GetTaskId(), runKey)
	return nil
}

// bridgeVerify proves the whole bridge chain survives process restarts: the
// token still resolves against the durable session, the run key still
// replays the first task (Core revalidating the installation again), and the
// provenance-bound event stream resumes to the terminal.
func bridgeVerify(ctx context.Context, client *http.Client, baseURL, token, taskID, runKey string) error {
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)

	replayRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey,
		Goal:           "Restart bridge goal",
	})
	replayRequest.Header().Set("X-WorkOS-Bridge-Token", token)
	replayed, err := bridge.RunAgentTask(ctx, replayRequest)
	if err != nil {
		return fmt.Errorf("bridge token/replay failed after restart: %w", err)
	}
	if replayed.Msg.GetTaskId() != taskID {
		return fmt.Errorf("bridge replay returned a different task: %s vs %s", replayed.Msg.GetTaskId(), taskID)
	}

	watch := connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{TaskId: taskID, AfterSequence: 0})
	watch.Header().Set("X-WorkOS-Bridge-Token", token)
	stream, err := bridge.WatchAgentTaskEvents(ctx, watch)
	if err != nil {
		return fmt.Errorf("bridge watch failed after restart: %w", err)
	}
	defer stream.Close()
	terminal := false
	for stream.Receive() {
		if stream.Msg().GetEvent().GetRunCompleted() != nil {
			terminal = true
			break
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("bridge event stream error: %w", err)
	}
	if !terminal {
		return errors.New("bridge event stream never reached the terminal")
	}
	fmt.Printf("bridge persistence verified for task %s\n", taskID)
	return nil
}

var _ = agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED
