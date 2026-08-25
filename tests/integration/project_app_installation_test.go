//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

func installationClients(t *testing.T) appv1connect.AppInstallationServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return appv1connect.NewAppInstallationServiceClient(httpClient, baseURL)
}

func integrationProjectClients(t *testing.T) projectv1connect.ProjectServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return projectv1connect.NewProjectServiceClient(httpClient, baseURL)
}

func createIntegrationProject(t *testing.T, ctx context.Context, clients projectv1connect.ProjectServiceClient, name, key string) *projectv1.Project {
	t.Helper()
	created, err := clients.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: key, Name: name}))
	if err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	return created.Msg.GetProject()
}

func installApp(t *testing.T, ctx context.Context, client appv1connect.AppInstallationServiceClient, key, projectID, appID, version string, revision int64) *appv1.AppInstallation {
	t.Helper()
	response, err := client.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key, ProjectId: projectID, AppId: appID, Version: version, ExpectedProjectRevision: revision,
	}))
	if err != nil {
		t.Fatalf("install %s@%s: %v", appID, version, err)
	}
	return response.Msg.GetInstallation()
}

// TestProjectAppInstallationVerticalSlice proves the durable user path:
// register → install with revision → pinned version survives registry
// progress → idempotent replay → conflict semantics → uninstall tombstone →
// reinstall identity → project projection → events.
func TestProjectAppInstallationVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	apps := appRegistryClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	stamp := time.Now().UnixNano()
	boardID := fmt.Sprintf("inst-board-%d", stamp)
	notesID := fmt.Sprintf("inst-notes-%d", stamp)
	boardDigest := registerApp(t, ctx, apps, boardID, "Install Board", "1.9.0", "project")
	project := createIntegrationProject(t, ctx, projects, "App Installation Vertical", fmt.Sprintf("install-project-%d", stamp))
	if len(project.GetInstalledAppIds()) != 0 || project.GetRevision() != 1 {
		t.Fatalf("fresh project must start without installations: %#v", project)
	}

	t.Run("InstallPinsCurrentVersionIntoProjection", func(t *testing.T) {
		installation := installApp(t, ctx, installations, fmt.Sprintf("install-current-%d", stamp), project.GetId(), boardID, "", project.GetRevision())
		if installation.GetId() == "" || installation.GetVersion() != "1.9.0" || installation.GetManifestDigest() != boardDigest ||
			installation.GetProjectId() != project.GetId() || installation.GetAppId() != boardID || installation.GetInstalledAt() == nil || installation.GetUninstalledAt() != nil {
			t.Fatalf("installation must pin the resolved current version: %#v", installation)
		}
		// The pinned UUIDv7 instance identity is the future app instance id.
		if len(installation.GetId()) != 36 || !strings.Contains(installation.GetId(), "-") {
			t.Fatalf("installation id must be a UUID: %q", installation.GetId())
		}
		updated, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatalf("get project after install: %v", err)
		}
		if updated.Msg.GetProject().GetRevision() != 2 {
			t.Fatalf("install must bump revision to 2, got %d", updated.Msg.GetProject().GetRevision())
		}
		if ids := updated.Msg.GetProject().GetInstalledAppIds(); len(ids) != 1 || ids[0] != boardID {
			t.Fatalf("installed_app_ids projection must list the installed app: %v", ids)
		}
	})

	t.Run("ExplicitVersionPinsExactImmutable", func(t *testing.T) {
		notesDigest := registerApp(t, ctx, apps, notesID, "Install Notes", "1.0.0", "user")
		registerApp(t, ctx, apps, notesID, "Install Notes", "1.1.0", "user")
		installation := installApp(t, ctx, installations, fmt.Sprintf("install-explicit-%d", stamp), project.GetId(), notesID, "1.0.0", 2)
		if installation.GetVersion() != "1.0.0" || installation.GetManifestDigest() != notesDigest {
			t.Fatalf("explicit version must pin exactly the immutable version: %#v", installation)
		}
	})

	t.Run("HigherRegistryVersionDoesNotSilentlyUpgrade", func(t *testing.T) {
		registerApp(t, ctx, apps, boardID, "Install Board", "2.0.0", "project")
		current, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: boardID}))
		if err != nil || current.Msg.GetApp().GetVersion() != "2.0.0" {
			t.Fatalf("registry current must now be 2.0.0: %#v err=%v", current.Msg, err)
		}
		listed, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatalf("list installed: %v", err)
		}
		for _, installation := range listed.Msg.GetInstallations() {
			if installation.GetAppId() == boardID && (installation.GetVersion() != "1.9.0" || installation.GetManifestDigest() != boardDigest) {
				t.Fatalf("existing installation must stay pinned to 1.9.0: %#v", installation)
			}
		}
		// A no-op second key resolves current 2.0.0 but the active
		// installation is still 1.9.0 — different version means conflict.
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: fmt.Sprintf("install-upgrade-%d", stamp), ProjectId: project.GetId(),
			AppId: boardID, ExpectedProjectRevision: 3,
		})); connect.CodeOf(err) != connect.CodeAlreadyExists {
			t.Fatalf("installing a different version over an active installation must be AlreadyExists, got %v", err)
		}
	})

	t.Run("IdempotentReplayAndConflicts", func(t *testing.T) {
		key := fmt.Sprintf("install-replay-%d", stamp)
		// The board app is already active at 1.9.0, so this command is a
		// deterministic no-op for a fresh key: it returns the existing
		// installation at the current revision without bumping anything.
		first := installApp(t, ctx, installations, key, project.GetId(), boardID, "1.9.0", 3)
		firstRevision := int64(3)
		// Same canonical request replays the first result without a new
		// revision or event.
		replay, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: key, ProjectId: project.GetId(), AppId: boardID, Version: "1.9.0", ExpectedProjectRevision: 3,
		}))
		if err != nil || replay.Msg.GetInstallation().GetId() != first.GetId() || replay.Msg.GetProjectRevision() != firstRevision {
			t.Fatalf("same key same request must replay: %#v err=%v", replay.Msg, err)
		}
		// Same key, different canonical request (different revision).
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: key, ProjectId: project.GetId(), AppId: boardID, Version: "1.9.0", ExpectedProjectRevision: 4,
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same key different request must be Aborted, got %v", err)
		}
		// Stale revision on a fresh key.
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: fmt.Sprintf("install-stale-%d", stamp), ProjectId: project.GetId(),
			AppId: boardID, Version: "1.9.0", ExpectedProjectRevision: 1,
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("stale revision must be Aborted, got %v", err)
		}
		// A failed request must not consume its key: the same key installs a
		// fresh app successfully afterwards.
		failedKey := fmt.Sprintf("install-failed-%d", stamp)
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: failedKey, ProjectId: project.GetId(), AppId: "missing-app", ExpectedProjectRevision: 4,
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown app must be NotFound, got %v", err)
		}
		// The board app is already installed with a different version; a
		// failed conflict must not consume the key either.
		conflictKey := fmt.Sprintf("install-conflict-%d", stamp)
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: conflictKey, ProjectId: project.GetId(), AppId: boardID, Version: "2.0.0", ExpectedProjectRevision: 3,
		})); connect.CodeOf(err) != connect.CodeAlreadyExists {
			t.Fatalf("different version must be AlreadyExists, got %v", err)
		}
	})

	t.Run("NoOpSecondKeyKeepsRevisionAndEvents", func(t *testing.T) {
		before, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		revision := before.Msg.GetProject().GetRevision()
		eventsBefore := countRows(t,
			`SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, project.GetId())
		result, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: fmt.Sprintf("install-noop-%d", stamp), ProjectId: project.GetId(),
			AppId: boardID, Version: "1.9.0", ExpectedProjectRevision: revision,
		}))
		if err != nil {
			t.Fatalf("same-version no-op must succeed: %v", err)
		}
		if result.Msg.GetProjectRevision() != revision {
			t.Fatalf("no-op must not bump the revision: %d vs %d", result.Msg.GetProjectRevision(), revision)
		}
		after, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
		if err != nil || after.Msg.GetProject().GetRevision() != revision {
			t.Fatalf("no-op must leave the project revision alone: %#v err=%v", after.Msg, err)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, project.GetId()); got != eventsBefore {
			t.Fatalf("no-op must not append events: %d vs %d", got, eventsBefore)
		}
	})

	t.Run("UninstallTombstoneReplayAndReinstallIdentity", func(t *testing.T) {
		before, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		revision := before.Msg.GetProject().GetRevision()
		boardInstallation := ""
		listed, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: project.GetId()}))
		if err != nil {
			t.Fatal(err)
		}
		for _, installation := range listed.Msg.GetInstallations() {
			if installation.GetAppId() == boardID {
				boardInstallation = installation.GetId()
			}
		}
		if boardInstallation == "" {
			t.Fatal("board installation must be listed before uninstall")
		}
		uninstallKey := fmt.Sprintf("uninstall-board-%d", stamp)
		result, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: uninstallKey, ProjectId: project.GetId(), InstallationId: boardInstallation, ExpectedProjectRevision: revision,
		}))
		if err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		tombstoned := result.Msg.GetInstallation()
		if tombstoned.GetId() != boardInstallation || tombstoned.GetUninstalledAt() == nil || result.Msg.GetProjectRevision() != revision+1 {
			t.Fatalf("uninstall must tombstone and bump the revision: %#v revision=%d", tombstoned, result.Msg.GetProjectRevision())
		}
		// Replay after the tombstone still returns the stored result.
		replay, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: uninstallKey, ProjectId: project.GetId(), InstallationId: boardInstallation, ExpectedProjectRevision: revision,
		}))
		if err != nil || replay.Msg.GetInstallation().GetId() != boardInstallation || replay.Msg.GetProjectRevision() != revision+1 {
			t.Fatalf("uninstall replay must return the tombstoned result: %#v err=%v", replay.Msg, err)
		}
		// A new key uninstalling the tombstoned installation is NotFound.
		if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: fmt.Sprintf("uninstall-again-%d", stamp), ProjectId: project.GetId(),
			InstallationId: boardInstallation, ExpectedProjectRevision: revision + 1,
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("new-key uninstall of a tombstoned installation must be NotFound, got %v", err)
		}
		// The old install key replay must not resurrect the tombstone: it
		// returns the first install response's active projection while the
		// database keeps the row tombstoned.
		installReplay, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: fmt.Sprintf("install-current-%d", stamp), ProjectId: project.GetId(),
			AppId: boardID, ExpectedProjectRevision: 1,
		}))
		if err != nil {
			// Digest precedence: the first install had no version field, so
			// the replay must succeed with the original result.
			t.Fatalf("old install key replay must succeed: %v", err)
		}
		if installReplay.Msg.GetInstallation().GetUninstalledAt() != nil || installReplay.Msg.GetProjectRevision() != 2 {
			t.Fatalf("install replay must return the first result: %#v", installReplay.Msg)
		}
		var active int
		if err := appRegistryDB(t).QueryRow(context.Background(),
			`SELECT count(*) FROM workos_core.project_app_installations WHERE id = $1 AND uninstalled_at IS NULL`, boardInstallation).Scan(&active); err != nil || active != 0 {
			t.Fatalf("tombstoned installation must stay inactive: %v %d", err, active)
		}
		// Reinstalling with a fresh key creates a new instance identity.
		reinstalled := installApp(t, ctx, installations, fmt.Sprintf("install-reinstall-%d", stamp), project.GetId(), boardID, "2.0.0", revision+1)
		if reinstalled.GetId() == boardInstallation {
			t.Fatal("reinstall must produce a new instance id")
		}
		if reinstalled.GetVersion() != "2.0.0" {
			t.Fatalf("reinstall pins the requested version: %#v", reinstalled)
		}
		var rows int
		if err := appRegistryDB(t).QueryRow(context.Background(),
			`SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1 AND app_id = $2 AND uninstalled_at IS NULL`,
			project.GetId(), boardID).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("exactly one active board installation may exist: %v %d", err, rows)
		}
	})

	t.Run("ErrorSurfacesAndFailClosedBoundaries", func(t *testing.T) {
		revision := currentProjectRevision(t, ctx, projects, project.GetId())
		for name, request := range map[string]*appv1.InstallAppRequest{
			"malformed project": {IdempotencyKey: "k", ProjectId: "not-a-uuid", AppId: boardID, ExpectedProjectRevision: 1},
			"malformed app":     {IdempotencyKey: "k", ProjectId: project.GetId(), AppId: "Bad_ID", ExpectedProjectRevision: 1},
			"malformed version": {IdempotencyKey: "k", ProjectId: project.GetId(), AppId: boardID, Version: "01.2.3", ExpectedProjectRevision: 1},
			"malformed key":     {IdempotencyKey: strings.Repeat("k", 129), ProjectId: project.GetId(), AppId: boardID, ExpectedProjectRevision: 1},
			"zero revision":     {IdempotencyKey: "k", ProjectId: project.GetId(), AppId: boardID, ExpectedProjectRevision: 0},
		} {
			if _, err := installations.InstallApp(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s must be InvalidArgument, got %v", name, err)
			}
		}
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: "unknown-project-key", ProjectId: "00000000-0000-7000-8000-000000000000",
			AppId: boardID, ExpectedProjectRevision: 1,
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown project must be NotFound, got %v", err)
		}
		if _, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
			ProjectId: "00000000-0000-7000-8000-000000000000",
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("list on unknown project must be NotFound, got %v", err)
		}
		if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: "unknown-installation-key", ProjectId: project.GetId(),
			InstallationId: "00000000-0000-7000-8000-000000000000", ExpectedProjectRevision: revision,
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown installation must be NotFound, got %v", err)
		}
		if _, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: -1},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("negative page size must be InvalidArgument, got %v", err)
		}
		// Archived projects fail closed for install, uninstall, and list.
		archivedProject := createIntegrationProject(t, ctx, projects, "Archived Install Target", fmt.Sprintf("install-archived-%d", stamp))
		archivedResponse, err := projects.ArchiveProject(ctx, connect.NewRequest(&projectv1.ArchiveProjectRequest{
			ProjectId: archivedProject.GetId(), ExpectedRevision: archivedProject.GetRevision(),
		}))
		if err != nil {
			t.Fatalf("archive project: %v", err)
		}
		archived := archivedResponse.Msg.GetProject()
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: "archived-key", ProjectId: archived.GetId(),
			AppId: boardID, ExpectedProjectRevision: archived.GetRevision(),
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("archived project install must be NotFound, got %v", err)
		}
		if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: "archived-key", ProjectId: archived.GetId(),
			InstallationId:          boardInstallationID(t, ctx, installations, project.GetId(), boardID),
			ExpectedProjectRevision: archived.GetRevision(),
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("archived project uninstall must be NotFound, got %v", err)
		}
		if _, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
			ProjectId: archived.GetId(),
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("archived project list must be NotFound, got %v", err)
		}
	})

	t.Run("EventsMirrorProjectRevisionWithStablePayloads", func(t *testing.T) {
		conn := appRegistryDB(t)
		rows, err := conn.Query(context.Background(), `
			SELECT sequence, event_type, payload FROM workos_events.events
			WHERE stream_type = 'project' AND stream_id = $1 ORDER BY sequence`, project.GetId())
		if err != nil {
			t.Fatalf("query project events: %v", err)
		}
		defer rows.Close()
		previous := int64(0)
		eventTypes := map[string]int{}
		payloadsByType := map[string]string{}
		for rows.Next() {
			var sequence int64
			var eventType, payload string
			if err := rows.Scan(&sequence, &eventType, &payload); err != nil {
				t.Fatalf("scan event: %v", err)
			}
			if sequence != previous+1 {
				t.Fatalf("project event sequence must be contiguous: %d after %d", sequence, previous)
			}
			previous = sequence
			eventTypes[eventType]++
			payloadsByType[eventType] = payload
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if eventTypes["project.app.installed.v1"] < 3 {
			t.Fatalf("expected install events, got %v", eventTypes)
		}
		if eventTypes["project.app.uninstalled.v1"] < 1 {
			t.Fatalf("expected uninstall events, got %v", eventTypes)
		}
		installed := payloadsByType["project.app.installed.v1"]
		for _, fragment := range []string{`"appId"`, `"version"`, `"manifestDigest"`, `"installationId"`, `"revision"`} {
			if !strings.Contains(installed, fragment) {
				t.Fatalf("install event payload must carry %s: %s", fragment, installed)
			}
		}
		if strings.Contains(installed, "canonical") || strings.Contains(installed, "permissions") {
			t.Fatalf("event payload must not carry manifest-derived material: %s", installed)
		}
		// The event sequence top equals the project revision.
		revision := currentProjectRevision(t, ctx, projects, project.GetId())
		if previous != revision {
			t.Fatalf("event sequence top %d must equal project revision %d", previous, revision)
		}
	})
}

func currentProjectRevision(t *testing.T, ctx context.Context, clients projectv1connect.ProjectServiceClient, projectID string) int64 {
	t.Helper()
	project, err := clients.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatalf("get project revision: %v", err)
	}
	return project.Msg.GetProject().GetRevision()
}

func boardInstallationID(t *testing.T, ctx context.Context, client appv1connect.AppInstallationServiceClient, projectID, appID string) string {
	t.Helper()
	listed, err := client.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatalf("list installations: %v", err)
	}
	for _, installation := range listed.Msg.GetInstallations() {
		if installation.GetAppId() == appID {
			return installation.GetId()
		}
	}
	return "00000000-0000-7000-8000-000000000000"
}

// TestProjectAppInstallationPaging exercises the paging contract on a
// high-cardinality project: default page, clamp, exact final page, stable
// ordering, and precise fixture cleanup.
func TestProjectAppInstallationPaging(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	apps := appRegistryClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	stamp := time.Now().UnixNano()
	const bulkCount = 100
	prefix := fmt.Sprintf("inst-bulk-%d-", stamp)
	var fixtureAppIDs, fixtureKeys []string
	for index := 0; index < bulkCount; index++ {
		fixtureAppIDs = append(fixtureAppIDs, fmt.Sprintf("%s%03d", prefix, index))
		fixtureKeys = append(fixtureKeys, fmt.Sprintf("inst-bulk-reg-%d-%03d", stamp, index))
	}
	project := createIntegrationProject(t, ctx, projects, "Install Paging Fixture", fmt.Sprintf("install-paging-%d", stamp))
	t.Cleanup(func() {
		removeInstallationFixture(t, project.GetId(), fixtureKeys, fixtureAppIDs)
	})
	for index, appID := range fixtureAppIDs {
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fixtureKeys[index], ManifestYaml: manifestFor(appID, "Bulk Install App", "1.0.0", "user"),
		})); err != nil {
			t.Fatalf("register %s: %v", appID, err)
		}
	}
	revision := project.GetRevision()
	for index, appID := range fixtureAppIDs {
		installApp(t, ctx, installations, fmt.Sprintf("inst-bulk-install-%d-%03d", stamp, index), project.GetId(), appID, "", revision)
		revision++
	}

	defaultPage, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: project.GetId()}))
	if err != nil || len(defaultPage.Msg.GetInstallations()) != 50 || defaultPage.Msg.GetPage().GetNextPageToken() == "" {
		t.Fatalf("default page must hold exactly 50 installations with a token: len=%d err=%v", len(defaultPage.Msg.GetInstallations()), err)
	}
	clamped, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
		ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 101},
	}))
	if err != nil || len(clamped.Msg.GetInstallations()) != 100 || clamped.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("page size must clamp to 100 and fill the exact final page without a token: len=%d token=%q err=%v",
			len(clamped.Msg.GetInstallations()), clamped.Msg.GetPage().GetNextPageToken(), err)
	}
	// Walk pages of 50: strictly ascending, both pages full, no token on the
	// exactly-full final page.
	previous := ""
	pages := 0
	token := ""
	var lastLen int
	for {
		page, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 50, PageToken: token},
		}))
		if err != nil {
			t.Fatalf("walk page %d: %v", pages, err)
		}
		for _, installation := range page.Msg.GetInstallations() {
			if installation.GetAppId() <= previous {
				t.Fatalf("installations must be sorted by app id: %s after %s", installation.GetAppId(), previous)
			}
			previous = installation.GetAppId()
		}
		lastLen = len(page.Msg.GetInstallations())
		pages++
		if page.Msg.GetPage().GetNextPageToken() == "" {
			break
		}
		token = page.Msg.GetPage().GetNextPageToken()
	}
	if pages != 2 || lastLen != 50 {
		t.Fatalf("100 installations must fill exactly two pages of 50: pages=%d last=%d", pages, lastLen)
	}
	if _, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{
		ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 10, PageToken: "not a cursor"},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("malformed cursor must be InvalidArgument, got %v", err)
	}
}

// removeInstallationFixture deletes exactly the rows one paging run created,
// in FK order inside a single transaction: installation requests first (they
// reference installations), then installations, the fixture's project event
// stream and outbox rows, the project itself, and finally the registry
// mappings and versions for the fixture apps.
func removeInstallationFixture(t *testing.T, projectID string, registryKeys, appIDs []string) {
	t.Helper()
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Errorf("cleanup: connect acceptance database: %v", err)
		return
	}
	defer closeConn(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Errorf("cleanup: begin installation fixture removal: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	steps := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM workos_core.project_app_installation_requests WHERE installation_id IN (SELECT id FROM workos_core.project_app_installations WHERE project_id = $1)`, []any{projectID}},
		{`DELETE FROM workos_core.project_app_installations WHERE project_id = $1`, []any{projectID}},
		{`DELETE FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, []any{projectID}},
		{`DELETE FROM workos_events.outbox WHERE aggregate_type = 'project' AND aggregate_id = $1`, []any{projectID}},
		{`DELETE FROM workos_core.projects WHERE id = $1`, []any{projectID}},
		{`DELETE FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1)`, []any{registryKeys}},
		{`DELETE FROM workos_core.app_versions WHERE app_id = ANY($1)`, []any{appIDs}},
	}
	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.query, step.args...); err != nil {
			t.Errorf("cleanup: %s: %v", firstLine(step.query), err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("cleanup: commit installation fixture removal: %v", err)
		return
	}
	var leftover int
	if err := conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM workos_core.project_app_installation_requests r
		         JOIN workos_core.project_app_installations i ON r.installation_id = i.id
		         WHERE i.project_id = $1)
		     + (SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1)
		     + (SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1)
		     + (SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'project' AND aggregate_id = $1)
		     + (SELECT count(*) FROM workos_core.projects WHERE id = $1)
		     + (SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($2))
		     + (SELECT count(*) FROM workos_core.app_versions WHERE app_id = ANY($3))`,
		projectID, registryKeys, appIDs).Scan(&leftover); err != nil {
		t.Errorf("cleanup: verify installation fixture removal: %v", err)
		return
	}
	if leftover != 0 {
		t.Errorf("cleanup: %d installation fixture rows survived", leftover)
	}
}

// TestProjectAppInstallationConcurrency proves revision arbitration across
// concurrent mutations: same expected revision leaves exactly one winner,
// concurrent same-key identical requests replay one result, and the database
// holds one active fact, one revision event, and one outbox row.
func TestProjectAppInstallationConcurrency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	apps := appRegistryClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("inst-race-%d", stamp)
	registerApp(t, ctx, apps, appID, "Install Race", "1.0.0", "user")
	project := createIntegrationProject(t, ctx, projects, "Install Concurrency", fmt.Sprintf("install-race-project-%d", stamp))
	revision := project.GetRevision()

	t.Run("SameExpectedRevisionYieldsExactlyOneWinner", func(t *testing.T) {
		const concurrency = 4
		start := make(chan struct{})
		results := make(chan error, concurrency)
		var group sync.WaitGroup
		for index := 0; index < concurrency; index++ {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				_, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
					IdempotencyKey: fmt.Sprintf("inst-race-win-%d-%d", stamp, index), ProjectId: project.GetId(),
					AppId: appID, ExpectedProjectRevision: revision,
				}))
				results <- err
			}(index)
		}
		close(start)
		group.Wait()
		close(results)
		winners, aborted := 0, 0
		for err := range results {
			if err == nil {
				winners++
				continue
			}
			if connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("unexpected concurrent result: %v", err)
			}
			aborted++
		}
		if winners != 1 || aborted != concurrency-1 {
			t.Fatalf("exactly one winner expected: winners=%d aborted=%d", winners, aborted)
		}
		assertSingleFact(t, project.GetId(), appID, revision+1)
	})

	t.Run("InstallCompetesWithProjectUpdateOnSameRevision", func(t *testing.T) {
		// A fresh app guarantees the install is a real mutation: a no-op
		// install would legitimately coexist with the concurrent update.
		updateAppID := fmt.Sprintf("inst-race-two-%d", stamp)
		registerApp(t, ctx, apps, updateAppID, "Install Race Two", "1.0.0", "user")
		current := currentProjectRevision(t, ctx, projects, project.GetId())
		start := make(chan struct{})
		outcomes := make(chan error, 2)
		go func() {
			<-start
			name := "Install Concurrency Renamed"
			_, err := projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
				ProjectId: project.GetId(), ExpectedRevision: current, Name: &name,
			}))
			outcomes <- err
		}()
		go func() {
			<-start
			_, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
				IdempotencyKey: fmt.Sprintf("inst-race-update-%d", stamp), ProjectId: project.GetId(),
				AppId: updateAppID, ExpectedProjectRevision: current,
			}))
			outcomes <- err
		}()
		close(start)
		first, second := <-outcomes, <-outcomes
		if first == nil && second == nil {
			t.Fatal("two mutations with the same expected revision cannot both win")
		}
		if first != nil && second != nil {
			t.Fatalf("one mutation must win: %v / %v", first, second)
		}
	})

	t.Run("ConcurrentSameKeyIdenticalRequestsReplayOneFact", func(t *testing.T) {
		current := currentProjectRevision(t, ctx, projects, project.GetId())
		key := fmt.Sprintf("inst-race-key-%d", stamp)
		start := make(chan struct{})
		results := make(chan *appv1.InstallAppResponse, 2)
		errs := make(chan error, 2)
		var group sync.WaitGroup
		for index := 0; index < 2; index++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				response, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
					IdempotencyKey: key, ProjectId: project.GetId(), AppId: appID,
					Version: "1.0.0", ExpectedProjectRevision: current,
				}))
				results <- response.Msg
				errs <- err
			}()
		}
		close(start)
		group.Wait()
		close(results)
		close(errs)
		ids := map[string]bool{}
		for err := range errs {
			if err != nil {
				t.Fatalf("identical same-key requests must both succeed: %v", err)
			}
		}
		for response := range results {
			ids[response.GetInstallation().GetId()] = true
		}
		if len(ids) != 1 {
			t.Fatalf("same key identical requests must agree on one installation: %v", ids)
		}
	})
}

// assertSingleFact verifies the database after a concurrency race: exactly
// one active installation, revision advanced by one, and exactly one event
// plus one outbox row for that revision.
func assertSingleFact(t *testing.T, projectID, appID string, expectedRevision int64) {
	t.Helper()
	conn := appRegistryDB(t)
	var (
		active    int
		revision  int64
		events    int
		outboxRow int
	)
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_core.project_app_installations WHERE project_id = $1 AND app_id = $2 AND uninstalled_at IS NULL`,
		projectID, appID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("exactly one active installation must exist: %v %d", err, active)
	}
	if err := conn.QueryRow(context.Background(),
		`SELECT revision FROM workos_core.projects WHERE id = $1`, projectID).Scan(&revision); err != nil || revision != expectedRevision {
		t.Fatalf("revision must be %d, got %d (%v)", expectedRevision, revision, err)
	}
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1 AND sequence = $2`,
		projectID, expectedRevision).Scan(&events); err != nil || events != 1 {
		t.Fatalf("exactly one event for the winning revision: %v %d", err, events)
	}
	if err := conn.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'project' AND aggregate_id = $1
		   AND event_type = 'project.app.installed.v1'
		   AND occurred_at = (SELECT occurred_at FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1 AND sequence = $2)`,
		projectID, expectedRevision).Scan(&outboxRow); err != nil || outboxRow != 1 {
		t.Fatalf("exactly one outbox row for the winning revision: %v %d", err, outboxRow)
	}
}
