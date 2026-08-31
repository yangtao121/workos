//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
)

// TestAppVersionTransitionAndRollback proves the owner-triggered version
// lifecycle (ADR-0012) over the real Gateway → Core → PostgreSQL chain:
// explicit transition, server-derived previous-pinned-version rollback,
// exact first-response replay, deterministic no-op, grant-compatibility fail
// closed, bounded history, and revision serialization. No direct database
// writes impersonate the user chain.
// Serial by design: this test registers new immutable registry versions as
// fixtures, and the parallel paging walk of the acceptance volume derives
// its padding from the live app count — a concurrent registration burst
// would race its exact-final-page arithmetic.
func TestAppVersionTransitionAndRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	apps := appRegistryClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("version-app-%d", stamp)
	digestV1 := registerApp(t, ctx, apps, appID, "Version App", "1.0.0", "user")
	registerApp(t, ctx, apps, appID, "Version App", "1.1.0", "user")
	registerApp(t, ctx, apps, appID, "Version App", "1.2.0", "user")
	project := createIntegrationProject(t, ctx, projects, "Version Lifecycle", fmt.Sprintf("version-project-%d", stamp))
	installation := installApp(t, ctx, installations, fmt.Sprintf("version-install-%d", stamp), project.GetId(), appID, "1.0.0", project.GetRevision())
	currentRevision := project.GetRevision() + 1
	// transitionRevision records the exact canonical request of the first
	// 1.1.0 transition so its replay subtest can resubmit it verbatim.
	transitionRevision := int64(0)

	t.Run("RollbackWithoutPreviousFailsClosed", func(t *testing.T) {
		response, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("rollback-empty-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision,
		}))
		if err == nil {
			t.Fatalf("rollback without a previous version must fail: %#v", response)
		}
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("rollback without previous must be FailedPrecondition, got %v", err)
		}
	})

	t.Run("TransitionPinsExactVersionAndAppendsHistory", func(t *testing.T) {
		transitionRevision = currentRevision
		response, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-11-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: transitionRevision,
			Version:                 "1.1.0",
		}))
		if err != nil {
			t.Fatalf("transition to 1.1.0: %v", err)
		}
		updated := response.Msg.GetInstallation()
		if updated.GetVersion() != "1.1.0" || updated.GetManifestDigest() == digestV1 || updated.GetManifestDigest() == "" {
			t.Fatalf("transition must pin the exact target version: %#v", updated)
		}
		if response.Msg.GetProjectRevision() != currentRevision+1 {
			t.Fatalf("transition must bump the project revision by one: got %d", response.Msg.GetProjectRevision())
		}
		currentRevision = response.Msg.GetProjectRevision()

		history := listHistory(t, ctx, installations, project.GetId(), installation.GetId())
		if len(history) != 2 || history[0].GetSource() != "install" || history[0].GetVersion() != "1.0.0" ||
			history[1].GetSource() != "transition" || history[1].GetVersion() != "1.1.0" {
			t.Fatalf("history must record install origin plus the transition: %#v", history)
		}
	})

	t.Run("SameKeyReplaysFirstResponseExactly", func(t *testing.T) {
		// The exact same canonical request — including the original expected
		// revision — replays the first response.
		response, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-11-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: transitionRevision,
			Version:                 "1.1.0",
		}))
		if err != nil {
			t.Fatalf("replay of a consumed transition key must succeed: %v", err)
		}
		if response.Msg.GetInstallation().GetVersion() != "1.1.0" || response.Msg.GetProjectRevision() != currentRevision {
			t.Fatalf("replay must return the first response exactly: %#v", response.Msg)
		}
		// Same key, different request: stable conflict.
		_, err = installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-11-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision,
			Version:                 "1.2.0",
		}))
		if connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same key with a different request must be Aborted, got %v", err)
		}
		// Same version transition under a fresh key is a deterministic no-op:
		// success, but neither revision nor history moves.
		response, err = installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-noop-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision,
			Version:                 "1.1.0",
		}))
		if err != nil {
			t.Fatalf("same-version transition must be a no-op success: %v", err)
		}
		if response.Msg.GetProjectRevision() != currentRevision {
			t.Fatalf("same-version transition must not move the revision: got %d", response.Msg.GetProjectRevision())
		}
		if history := listHistory(t, ctx, installations, project.GetId(), installation.GetId()); len(history) != 2 {
			t.Fatalf("same-version transition must not append history: %#v", history)
		}
	})

	t.Run("RollbackRestoresExactPreviousPinnedVersion", func(t *testing.T) {
		rollbackRevision := currentRevision
		response, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("rollback-1-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: rollbackRevision,
		}))
		if err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if response.Msg.GetRolledBackToVersion() != "1.0.0" || response.Msg.GetInstallation().GetVersion() != "1.0.0" {
			t.Fatalf("rollback must restore the exact previous pin: %#v", response.Msg)
		}
		if response.Msg.GetProjectRevision() != currentRevision+1 {
			t.Fatalf("rollback must bump the project revision by one: got %d", response.Msg.GetProjectRevision())
		}
		currentRevision = response.Msg.GetProjectRevision()

		// Exact replay with the same key and the original canonical request
		// (including its expected revision) returns the first response.
		replay, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("rollback-1-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: rollbackRevision,
		}))
		if err != nil {
			t.Fatalf("rollback replay: %v", err)
		}
		if replay.Msg.GetRolledBackToVersion() != "1.0.0" || replay.Msg.GetProjectRevision() != currentRevision {
			t.Fatalf("rollback replay must return the first response: %#v", replay.Msg)
		}

		// The next rollback walks one more step back through the history:
		// the most recent snapshot different from the current identity.
		second, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("rollback-2-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision,
		}))
		if err != nil {
			t.Fatalf("second rollback: %v", err)
		}
		if second.Msg.GetRolledBackToVersion() != "1.1.0" {
			t.Fatalf("second rollback must select the most recent different snapshot: %#v", second.Msg)
		}
		currentRevision = second.Msg.GetProjectRevision()
	})

	t.Run("GrantExpansionFailsClosed", func(t *testing.T) {
		grantedID := fmt.Sprintf("version-granted-%d", stamp)
		// v1 requests exactly one permission; v2 requests a different one, so
		// keeping the granted set would expand authority.
		digest := registerAppManifest(t, ctx, apps, grantedID, "Version Granted", "1.0.0", "user", "permissions: [artifact.read]")
		registerAppManifest(t, ctx, apps, grantedID, "Version Granted", "2.0.0", "user", "permissions: [knowledge.read]")
		grantedProject := createIntegrationProject(t, ctx, projects, "Version Granted", fmt.Sprintf("version-granted-project-%d", stamp))
		granted := installApp(t, ctx, installations, fmt.Sprintf("version-granted-install-%d", stamp), grantedProject.GetId(), grantedID, "1.0.0", grantedProject.GetRevision())
		if granted.GetManifestDigest() != digest {
			t.Fatalf("granted install must pin v1: %#v", granted)
		}
		// Grant exactly the requested subset via SetAppGrants.
		grants := setGrants(t, ctx, installations, fmt.Sprintf("version-granted-grants-%d", stamp), grantedProject.GetId(), granted.GetId(), grantedProject.GetRevision()+1, []string{"artifact.read"})

		_, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("version-granted-transition-%d", stamp),
			ProjectId:               grantedProject.GetId(),
			InstallationId:          granted.GetId(),
			ExpectedProjectRevision: grants.GetProjectRevision(),
			Version:                 "2.0.0",
		}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("transition that would expand permissions must fail FailedPrecondition, got %v", err)
		}
		fresh, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: grantedProject.GetId()}))
		if err != nil {
			t.Fatalf("list after failed transition: %v", err)
		}
		for _, candidate := range fresh.Msg.GetInstallations() {
			if candidate.GetId() == granted.GetId() && (candidate.GetVersion() != "1.0.0" || candidate.GetGrantRevision() != 2) {
				t.Fatalf("failed transition must leave zero side effects: %#v", candidate)
			}
		}
	})

	t.Run("UnknownTargetVersionIsNotFound", func(t *testing.T) {
		_, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-unknown-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision,
			Version:                 "9.9.9",
		}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown target version must be NotFound, got %v", err)
		}
	})

	t.Run("StaleRevisionIsAborted", func(t *testing.T) {
		_, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("transition-stale-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          installation.GetId(),
			ExpectedProjectRevision: currentRevision - 1,
			Version:                 "1.2.0",
		}))
		if connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("stale expected revision must be Aborted, got %v", err)
		}
	})

	t.Run("ForeignInstallationIsNotFound", func(t *testing.T) {
		_, err := installations.RollbackAppVersion(ctx, connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey:          fmt.Sprintf("rollback-foreign-%d", stamp),
			ProjectId:               project.GetId(),
			InstallationId:          "01990000-0000-7000-8000-0000000000ff",
			ExpectedProjectRevision: currentRevision,
		}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("foreign installation rollback must be NotFound, got %v", err)
		}
	})

	t.Run("HistoryIsBoundedAndPageable", func(t *testing.T) {
		// Walk through 1.2.0 and back down repeatedly to push past the
		// bounded history limit.
		targets := []string{"1.2.0", "1.1.0"}
		for index := 0; index < 22; index++ {
			target := targets[index%len(targets)]
			response, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{
				IdempotencyKey:          fmt.Sprintf("transition-bound-%d-%d", stamp, index),
				ProjectId:               project.GetId(),
				InstallationId:          installation.GetId(),
				ExpectedProjectRevision: currentRevision,
				Version:                 target,
			}))
			if err != nil {
				t.Fatalf("bounded transition %d to %s: %v", index, target, err)
			}
			currentRevision = response.Msg.GetProjectRevision()
		}
		all := listHistory(t, ctx, installations, project.GetId(), installation.GetId())
		if len(all) > 20 {
			t.Fatalf("history must stay bounded at 20, got %d", len(all))
		}
		if len(all) < 20 {
			t.Fatalf("history must keep exactly the newest snapshots: %d", len(all))
		}
		for index := 1; index < len(all); index++ {
			if all[index].GetSequence() <= all[index-1].GetSequence() {
				t.Fatalf("history sequences must strictly increase: %#v", all)
			}
		}
		if all[0].GetSequence() <= 1 {
			t.Fatalf("trim must have dropped the oldest snapshots: %#v", all[0])
		}
		pageOne := listHistoryPage(t, ctx, installations, project.GetId(), installation.GetId())
		if len(pageOne) != 8 || pageOne[len(pageOne)-1].GetSequence() <= pageOne[0].GetSequence() {
			t.Fatalf("first page must hold the oldest 8 snapshots in order: %#v", pageOne)
		}
	})
}

func listHistory(t *testing.T, ctx context.Context, installations appv1connect.AppInstallationServiceClient, projectID, installationID string) []*appv1.AppInstallationVersionSnapshot {
	t.Helper()
	var all []*appv1.AppInstallationVersionSnapshot
	token := ""
	for {
		response, err := installations.ListAppVersionHistory(ctx, connect.NewRequest(&appv1.ListAppVersionHistoryRequest{
			ProjectId:      projectID,
			InstallationId: installationID,
			Page:           &commonv1.PageRequest{PageToken: token, PageSize: 20},
		}))
		if err != nil {
			t.Fatalf("list version history: %v", err)
		}
		all = append(all, response.Msg.GetSnapshots()...)
		if response.Msg.GetPage().GetNextPageToken() == "" {
			return all
		}
		token = response.Msg.GetPage().GetNextPageToken()
	}
}

func listHistoryPage(t *testing.T, ctx context.Context, installations appv1connect.AppInstallationServiceClient, projectID, installationID string) []*appv1.AppInstallationVersionSnapshot {
	t.Helper()
	response, err := installations.ListAppVersionHistory(ctx, connect.NewRequest(&appv1.ListAppVersionHistoryRequest{
		ProjectId:      projectID,
		InstallationId: installationID,
		Page:           &commonv1.PageRequest{PageSize: 8},
	}))
	if err != nil {
		t.Fatalf("list first history page: %v", err)
	}
	return response.Msg.GetSnapshots()
}

// registerAppManifest registers one exact version with a custom permissions
// line and returns the pinned manifest digest.
func registerAppManifest(t *testing.T, ctx context.Context, client appv1connect.AppRegistryServiceClient, appID, name, version, scope, permissions string) string {
	t.Helper()
	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: %s
version: %s
scope: %s
runtime:
  type: container
  image: localhost/workos-e2e-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-e2e-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
%s
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
maintainer: {}
`, appID, name, version, scope, permissions)
	response, err := client.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("register-manifest-%s-%s-%d", appID, version, time.Now().UnixNano()),
		ManifestYaml:   []byte(manifest),
	}))
	if err != nil {
		t.Fatalf("register manifest %s@%s: %v", appID, version, err)
	}
	return response.Msg.GetApp().GetManifestDigest()
}

func setGrants(t *testing.T, ctx context.Context, installations appv1connect.AppInstallationServiceClient, key, projectID, installationID string, revision int64, granted []string) *appv1.SetAppGrantsResponse {
	t.Helper()
	response, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
		IdempotencyKey: key, ProjectId: projectID, InstallationId: installationID,
		ExpectedProjectRevision: revision, GrantedPermissions: granted,
	}))
	if err != nil {
		t.Fatalf("set grants: %v", err)
	}
	return response.Msg
}
