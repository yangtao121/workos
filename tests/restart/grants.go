package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"

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

// grantsSeed drives the mutable-grant lifecycle once before the restart: a
// run+watch app is installed with both grants, a surface is opened and one
// real task runs through the Fake Harness, then every permission is revoked
// with SetAppGrants (epoch 2). The old bridge token, the surface create key,
// the set-grants key, and the post-set project revision are printed for
// grants-verify.
func grantsSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-grants-%d", stamp)

	created, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-artifact-%d", stamp),
		Artifact:       &artifactv1.Artifact{Title: "Restart Grants"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("<!doctype html><title>Restart Grants</title><div id=\"root\"></div>")},
			},
		},
	}))
	if err != nil {
		return fmt.Errorf("create grants artifact: %w", err)
	}
	artifact := created.Msg.GetArtifact()

	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Restart Grants App
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
		IdempotencyKey: fmt.Sprintf("restart-grants-register-%d", stamp), ManifestYaml: []byte(manifest),
	})); err != nil {
		return fmt.Errorf("register restart grants app: %w", err)
	}
	projectResponse, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-project-%d", stamp), Name: "Restart Grants",
	}))
	if err != nil {
		return fmt.Errorf("create restart grants project: %w", err)
	}
	project := projectResponse.Msg.GetProject()
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("restart-grants-install-%d", stamp),
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		return fmt.Errorf("install restart grants app: %w", err)
	}
	if installed.Msg.GetInstallation().GetGrantRevision() != 1 {
		return fmt.Errorf("fresh installation must start at grant revision 1: %d", installed.Msg.GetInstallation().GetGrantRevision())
	}
	surfaceKey := fmt.Sprintf("restart-grants-surface-%d", stamp)
	opened, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    surfaceKey,
		AppInstanceId:     installed.Msg.GetInstallation().GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		return fmt.Errorf("open restart grants surface: %w", err)
	}
	token := opened.Msg.GetSession().GetBridgeToken()
	if token == "" {
		return errors.New("restart grants surface has no bridge token")
	}
	runRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-run-%d", stamp),
		Goal:           "Restart grants goal",
	})
	runRequest.Header().Set("X-WorkOS-Bridge-Token", token)
	if _, err := bridge.RunAgentTask(ctx, runRequest); err != nil {
		return fmt.Errorf("pre-revocation bridge run failed: %w", err)
	}
	// Revoke every permission: epoch 2, project revision advanced once.
	setKey := fmt.Sprintf("restart-grants-set-%d", stamp)
	revoked, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
		IdempotencyKey:          setKey,
		ProjectId:               project.GetId(),
		InstallationId:          installed.Msg.GetInstallation().GetId(),
		ExpectedProjectRevision: installed.Msg.GetProjectRevision(),
	}))
	if err != nil {
		return fmt.Errorf("revoke restart grants: %w", err)
	}
	if revoked.Msg.GetInstallation().GetGrantRevision() != 2 || len(revoked.Msg.GetInstallation().GetGrantedPermissions()) != 0 {
		return fmt.Errorf("revoke did not reach epoch 2 with an empty grant: %#v", revoked.Msg.GetInstallation())
	}
	fmt.Printf("%s %s %s %s %s %d\n",
		token, project.GetId(), installed.Msg.GetInstallation().GetId(), surfaceKey, setKey, revoked.Msg.GetProjectRevision())
	return nil
}

// grantsVerify proves the mutable-grant facts survive Core/runtime restarts:
// the old token is denied server-side at the persisted epoch check, the old
// create key fails closed instead of minting a superseded-epoch credential,
// the set-grants key replays its exact first response, a fresh surface of the
// revoked installation has no bridge capabilities, and a fresh re-grant plus
// reopen works end to end through the Fake Harness.
func grantsVerify(ctx context.Context, client *http.Client, baseURL, token, projectID, installationID, surfaceKey, setKey string, setRevision int64) error {
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	bridge := bridgev1connect.NewAppBridgeServiceClient(client, baseURL)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)

	// The pre-restart token is denied server-side at the grant epoch check.
	staleRun := connect.NewRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-stale-%d", time.Now().UnixNano()),
		Goal:           "must be denied after restart",
	})
	staleRun.Header().Set("X-WorkOS-Bridge-Token", token)
	_, staleErr := bridge.RunAgentTask(ctx, staleRun)
	if connect.CodeOf(staleErr) != connect.CodePermissionDenied {
		return fmt.Errorf("stale-epoch token after restart: %v", staleErr)
	}

	// The old create key fails closed: no token is minted for the superseded
	// epoch after the restart either.
	_, replayErr := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: surfaceKey, AppInstanceId: installationID, ProjectId: projectID,
		DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	}))
	if connect.CodeOf(replayErr) != connect.CodeFailedPrecondition {
		return fmt.Errorf("old create key after restart: %v", replayErr)
	}

	// The set-grants key replays its exact first response: empty grant at
	// epoch 2 and the first response's project revision.
	replayed, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
		IdempotencyKey: setKey, ProjectId: projectID, InstallationId: installationID,
		ExpectedProjectRevision: setRevision - 1,
	}))
	if err != nil {
		return fmt.Errorf("set-grants key replay after restart: %w", err)
	}
	if replayed.Msg.GetInstallation().GetGrantRevision() != 2 ||
		len(replayed.Msg.GetInstallation().GetGrantedPermissions()) != 0 ||
		replayed.Msg.GetProjectRevision() != setRevision {
		return fmt.Errorf("set-grants replay diverged after restart: %#v", replayed.Msg)
	}

	// A fresh surface of the revoked installation carries no capabilities.
	revokedSurface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-reopen-revoked-%d", time.Now().UnixNano()),
		AppInstanceId:  installationID, ProjectId: projectID,
		DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	}))
	if err != nil {
		return fmt.Errorf("reopen revoked installation after restart: %w", err)
	}
	if capabilities := revokedSurface.Msg.GetSession().GetBridgeCapabilities(); len(capabilities) != 0 {
		return fmt.Errorf("revoked installation must grant no capabilities after restart: %v", capabilities)
	}

	// A fresh re-grant (epoch 3) and a fresh surface run one task through the
	// Fake Harness end to end: the new epoch works after the restart.
	current, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("get project after restart: %w", err)
	}
	regranted, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
		IdempotencyKey:          fmt.Sprintf("restart-grants-regrant-%d", time.Now().UnixNano()),
		ProjectId:               projectID,
		InstallationId:          installationID,
		ExpectedProjectRevision: current.Msg.GetProject().GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		return fmt.Errorf("re-grant after restart: %w", err)
	}
	if regranted.Msg.GetInstallation().GetGrantRevision() != 3 {
		return fmt.Errorf("re-grant must reach epoch 3 after restart: %d", regranted.Msg.GetInstallation().GetGrantRevision())
	}
	reopened, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: fmt.Sprintf("restart-grants-reopen-%d", time.Now().UnixNano()),
		AppInstanceId:  installationID, ProjectId: projectID,
		DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	}))
	if err != nil {
		return fmt.Errorf("reopen after re-grant: %w", err)
	}
	freshToken := reopened.Msg.GetSession().GetBridgeToken()
	if freshToken == "" {
		return errors.New("reopened surface has no bridge token")
	}
	if capabilities := reopened.Msg.GetSession().GetBridgeCapabilities(); len(capabilities) != 2 {
		return fmt.Errorf("reopened surface must carry both capabilities: %v", capabilities)
	}
	runKey := fmt.Sprintf("restart-grants-run-after-%d", time.Now().UnixNano())
	runRequest := connect.NewRequest(&bridgev1.RunAgentTaskRequest{IdempotencyKey: runKey, Goal: "Restart grants goal after re-grant"})
	runRequest.Header().Set("X-WorkOS-Bridge-Token", freshToken)
	run, err := bridge.RunAgentTask(ctx, runRequest)
	if err != nil {
		return fmt.Errorf("bridge run after re-grant: %w", err)
	}
	watch := connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{TaskId: run.Msg.GetTaskId()})
	watch.Header().Set("X-WorkOS-Bridge-Token", freshToken)
	stream, err := bridge.WatchAgentTaskEvents(ctx, watch)
	if err != nil {
		return fmt.Errorf("bridge watch after re-grant: %w", err)
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
		return fmt.Errorf("bridge event stream after re-grant: %w", err)
	}
	if !terminal {
		return errors.New("bridge event stream never reached the terminal after restart")
	}
	fmt.Printf("mutable grants persistence verified for installation %s\n", installationID)
	return nil
}
