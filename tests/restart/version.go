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
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// versionSeed drives the app version lifecycle once before the restart
// (ADR-0012): two immutable web-bundle versions are registered, v1 is
// installed, the owner explicitly transitions to v2, and then rolls back to
// the previous pinned v1. The seeds' canonical keys and the original
// expected revisions are printed so version-verify can replay them verbatim
// after the restart.
func versionSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-version-%d", stamp)

	createBundle := func(marker string) (*artifactv1.Artifact, error) {
		created, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
			IdempotencyKey: fmt.Sprintf("restart-version-artifact-%s-%d", marker, stamp),
			Artifact:       &artifactv1.Artifact{Title: "Restart Version " + marker},
			WebBundle: &artifactv1.WebBundleContent{
				Entrypoint: "index.html",
				Files: []*artifactv1.WebBundleFile{
					{Path: "index.html", Content: []byte(fmt.Sprintf("<!doctype html><title>Restart Version %s</title><div id=\"root\">%s</div>", marker, marker))},
				},
			},
		}))
		if err != nil {
			return nil, err
		}
		return created.Msg.GetArtifact(), nil
	}
	artifactV1, err := createBundle("v1")
	if err != nil {
		return fmt.Errorf("create version v1 artifact: %w", err)
	}
	artifactV2, err := createBundle("v2")
	if err != nil {
		return fmt.Errorf("create version v2 artifact: %w", err)
	}

	register := func(version string, artifact *artifactv1.Artifact) error {
		manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Restart Version App
version: %s
scope: user
runtime:
  type: web-bundle
  artifactId: %s
  artifactDigest: %s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
permissions: []
resources: {}
health: {}
maintainer: {}
`, appID, version, artifact.GetId(), artifact.GetDigest())
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("restart-version-register-%s-%d", version, stamp), ManifestYaml: []byte(manifest),
		})); err != nil {
			return fmt.Errorf("register %s: %w", version, err)
		}
		return nil
	}
	if err := register("1.0.0", artifactV1); err != nil {
		return err
	}
	if err := register("1.1.0", artifactV2); err != nil {
		return err
	}

	projectResponse, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-version-project-%d", stamp), Name: "Restart Version",
	}))
	if err != nil {
		return fmt.Errorf("create version project: %w", err)
	}
	project := projectResponse.Msg.GetProject()
	installResponse, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-version-install-%d", stamp), ProjectId: project.GetId(),
		AppId: appID, Version: "1.0.0", ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		return fmt.Errorf("install version v1: %w", err)
	}
	installation := installResponse.Msg.GetInstallation()

	transitionRevision := installResponse.Msg.GetProjectRevision()
	transitionKey := fmt.Sprintf("restart-version-transition-%d", stamp)
	transition, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
		IdempotencyKey: transitionKey, ProjectId: project.GetId(), InstallationId: installation.GetId(),
		ExpectedProjectRevision: transitionRevision, Version: "1.1.0",
	}))
	if err != nil {
		return fmt.Errorf("transition to v2: %w", err)
	}
	if transition.Msg.GetInstallation().GetVersion() != "1.1.0" {
		return errors.New("transition seed did not pin v2")
	}

	rollbackRevision := transition.Msg.GetProjectRevision()
	rollbackKey := fmt.Sprintf("restart-version-rollback-%d", stamp)
	rollback, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
		IdempotencyKey: rollbackKey, ProjectId: project.GetId(), InstallationId: installation.GetId(),
		ExpectedProjectRevision: rollbackRevision,
	}))
	if err != nil {
		return fmt.Errorf("rollback to previous: %w", err)
	}
	if rollback.Msg.GetRolledBackToVersion() != "1.0.0" {
		return errors.New("rollback seed did not restore v1")
	}

	fmt.Printf("%s %s %s %s %d %d", project.GetId(), installation.GetId(), transitionKey, rollbackKey, transitionRevision, rollbackRevision)
	return nil
}

// versionVerify re-reads the version facts after the restart and replays
// both canonical commands: the history is intact, the installation still
// pins the rolled-back v1, and both consumed keys replay their exact first
// responses (version identity included).
func versionVerify(ctx context.Context, client *http.Client, baseURL, projectID, installationID, transitionKey, rollbackKey string, transitionRevision, rollbackRevision int64) error {
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	listed, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("list installations after restart: %w", err)
	}
	var found *appv1.AppInstallation
	for _, candidate := range listed.Msg.GetInstallations() {
		if candidate.GetId() == installationID {
			found = candidate
			break
		}
	}
	if found == nil {
		return errors.New("version installation vanished across the restart")
	}
	if found.GetVersion() != "1.0.0" {
		return fmt.Errorf("installation must still pin the rolled-back v1, got %q", found.GetVersion())
	}

	history, err := installations.ListAppVersionHistory(ctx, connect.NewRequest(&appv1.ListAppVersionHistoryRequest{
		ProjectId: projectID, InstallationId: installationID,
	}))
	if err != nil {
		return fmt.Errorf("list version history after restart: %w", err)
	}
	snapshots := history.Msg.GetSnapshots()
	if len(snapshots) != 3 ||
		snapshots[0].GetSource() != "install" || snapshots[0].GetVersion() != "1.0.0" ||
		snapshots[1].GetSource() != "transition" || snapshots[1].GetVersion() != "1.1.0" ||
		snapshots[2].GetSource() != "rollback" || snapshots[2].GetVersion() != "1.0.0" {
		return fmt.Errorf("version history must survive the restart intact: %#v", snapshots)
	}

	transitionReplay, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
		IdempotencyKey: transitionKey, ProjectId: projectID, InstallationId: installationID,
		ExpectedProjectRevision: transitionRevision, Version: "1.1.0",
	}))
	if err != nil {
		return fmt.Errorf("transition replay after restart: %w", err)
	}
	if transitionReplay.Msg.GetInstallation().GetVersion() != "1.1.0" || transitionReplay.Msg.GetProjectRevision() != transitionRevision+1 {
		return fmt.Errorf("transition replay must return the first response: %#v", transitionReplay.Msg)
	}

	rollbackReplay, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
		IdempotencyKey: rollbackKey, ProjectId: projectID, InstallationId: installationID,
		ExpectedProjectRevision: rollbackRevision,
	}))
	if err != nil {
		return fmt.Errorf("rollback replay after restart: %w", err)
	}
	if rollbackReplay.Msg.GetRolledBackToVersion() != "1.0.0" || rollbackReplay.Msg.GetProjectRevision() != rollbackRevision+1 {
		return fmt.Errorf("rollback replay must return the first response: %#v", rollbackReplay.Msg)
	}
	fmt.Printf("version transition and rollback persistence verified for installation %s\n", installationID)
	return nil
}
