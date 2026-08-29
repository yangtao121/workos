//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
)

// gatewayOwnerID is the fixed DevBypass identity the acceptance gateway
// injects (compose.yaml WORKOS_OWNER_ID); owner-scoped DB assertions use it.
const gatewayOwnerID = "0198d7ea-2110-7c42-b659-c5e4d73bc337"

// mustSetGrants issues one SetAppGrants command and fails the test on
// transport-level surprises, keeping call sites readable.
func mustSetGrants(t *testing.T, ctx context.Context, client appv1connect.AppInstallationServiceClient, key, projectID, installationID string, revision int64, grants []string) *appv1.SetAppGrantsResponse {
	t.Helper()
	response, err := client.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
		IdempotencyKey: key, ProjectId: projectID, InstallationId: installationID,
		ExpectedProjectRevision: revision, GrantedPermissions: grants,
	}))
	if err != nil {
		t.Fatalf("set app grants (%s): %v", key, err)
	}
	return response.Msg
}

// installationGrantRow reads the durable grant facts of one installation.
func installationGrantRow(t *testing.T, installationID string) (granted []string, revision int64, uninstalled bool) {
	t.Helper()
	if err := appRegistryDB(t).QueryRow(context.Background(),
		`SELECT granted_permissions, grant_revision, (uninstalled_at IS NOT NULL) FROM workos_core.project_app_installations WHERE id = $1`,
		installationID,
	).Scan(&granted, &revision, &uninstalled); err != nil {
		t.Fatalf("read installation grant row: %v", err)
	}
	return granted, revision, uninstalled
}

// grantsEventCount counts project.app.grants.updated.v1 rows at one sequence.
func grantsEventCount(t *testing.T, projectID string, sequence int64) int {
	t.Helper()
	return countRows(t, `SELECT count(*) FROM workos_events.events
		WHERE stream_type = 'project' AND stream_id = $1 AND sequence = $2 AND event_type = 'project.app.grants.updated.v1'`,
		projectID, sequence)
}

// grantsEventPayload decodes one grants-updated event payload as a map.
func grantsEventPayload(t *testing.T, projectID string, sequence int64) map[string]any {
	t.Helper()
	var payload string
	if err := appRegistryDB(t).QueryRow(context.Background(),
		`SELECT payload FROM workos_events.events
		WHERE stream_type = 'project' AND stream_id = $1 AND sequence = $2 AND event_type = 'project.app.grants.updated.v1'`,
		projectID, sequence,
	).Scan(&payload); err != nil {
		t.Fatalf("read grants event payload: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode grants event payload: %v", err)
	}
	return decoded
}

// projectRow reads the project row's revision and updated_at; the no-op
// assertions compare the exact timestamp.
func projectRow(t *testing.T, projectID string) (int64, time.Time) {
	t.Helper()
	var revision int64
	var updatedAt time.Time
	if err := appRegistryDB(t).QueryRow(context.Background(),
		`SELECT revision, updated_at FROM workos_core.projects WHERE id = $1`, projectID,
	).Scan(&revision, &updatedAt); err != nil {
		t.Fatalf("read project row: %v", err)
	}
	return revision, updatedAt
}

// storedInstallationRequest reads one consumed idempotency mapping row.
func storedInstallationRequest(t *testing.T, key string) (command string, resultGranted []string, resultGrantRevision, projectRevision int64) {
	t.Helper()
	if err := appRegistryDB(t).QueryRow(context.Background(),
		`SELECT command, result_granted_permissions, result_grant_revision, project_revision
		FROM workos_core.project_app_installation_requests WHERE owner_user_id = $1 AND idempotency_key = $2`,
		gatewayOwnerID, key,
	).Scan(&command, &resultGranted, &resultGrantRevision, &projectRevision); err != nil {
		t.Fatalf("read installation request mapping (%s): %v", key, err)
	}
	return command, resultGranted, resultGrantRevision, projectRevision
}

// sessionGrantRevision reads the runtime-owned persisted epoch snapshot of
// one surface session row.
func sessionGrantRevision(t *testing.T, sessionID string) int64 {
	t.Helper()
	var revision int64
	if err := appRegistryDB(t).QueryRow(context.Background(),
		`SELECT installation_grant_revision FROM workos_runtime.surface_sessions WHERE id = $1`, sessionID,
	).Scan(&revision); err != nil {
		t.Fatalf("read surface session grant revision: %v", err)
	}
	return revision
}

// grantsFixture is one installed bridge-capable app ready for grant mutation.
type grantsFixture struct {
	projectID      string
	installationID string
	appID          string
	installKey     string
	revision       int64 // project revision after install
}

// newMutableGrantsFixture registers a run+watch bridge app, creates a fresh
// project, and installs the app with both capabilities granted. The returned
// install key enables historical-replay assertions after later mutations.
func newMutableGrantsFixture(t *testing.T, ctx context.Context, label string) grantsFixture {
	t.Helper()
	artifacts := artifactClients(t)
	registry := appRegistryClients(t)
	installations := installationClients(t)
	projects := integrationProjectClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("grants-%s-%d", label, stamp)
	artifact := createArtifact(t, ctx, artifacts, fmt.Sprintf("grants-%s-artifact-%d", label, stamp), "Grants App", bundleFiles(), "index.html")
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("grants-%s-register-%d", label, stamp),
		ManifestYaml:   bridgeAgentManifest(appID, "Grants App", artifact.GetId(), artifact.GetDigest()),
	})); err != nil {
		t.Fatalf("register grants app: %v", err)
	}
	project := createIntegrationProject(t, ctx, projects, "Mutable Grants "+label, fmt.Sprintf("grants-%s-project-%d", label, stamp))
	installKey := fmt.Sprintf("grants-%s-install-%d", label, stamp)
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          installKey,
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		t.Fatalf("install grants app: %v", err)
	}
	installation := installed.Msg.GetInstallation()
	if installation.GetGrantRevision() != 1 {
		t.Fatalf("fresh installation must start at grant revision 1: %#v", installation)
	}
	return grantsFixture{
		projectID: project.GetId(), installationID: installation.GetId(), appID: appID,
		installKey: installKey, revision: installed.Msg.GetProjectRevision(),
	}
}

// TestMutableProjectAppGrantsVerticalSlice proves the SetAppGrants semantics
// end to end through the real gateway, Core, and PostgreSQL: the real-change
// transaction facts (both revisions, row, event, outbox, payload), the
// deterministic same-set no-op that still consumes its key, precise replay of
// the first response after later mutations, key-consumption rules for failed
// requests, the sanitized error matrix, and same-key cross-command/project
// conflicts.
func TestMutableProjectAppGrantsVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	installations := installationClients(t)
	fixture := newMutableGrantsFixture(t, ctx, "vertical")

	// Grant state entering each subtest (they run in order):
	//   install: [watch, run] epoch 1, project revision 2.

	t.Run("RealChangeBumpsBothRevisionsExactlyOnce", func(t *testing.T) {
		beforeRevision, beforeUpdated := projectRow(t, fixture.projectID)
		response := mustSetGrants(t, ctx, installations,
			fmt.Sprintf("grants-real-%d", time.Now().UnixNano()), fixture.projectID, fixture.installationID,
			beforeRevision, []string{"agent.task.run"})

		applied := response.GetInstallation()
		if got := applied.GetGrantedPermissions(); len(got) != 1 || got[0] != "agent.task.run" {
			t.Fatalf("applied grant wrong: %v", got)
		}
		if applied.GetGrantRevision() != 2 {
			t.Fatalf("grant revision must be exactly 2 after one real change, got %d", applied.GetGrantRevision())
		}
		if response.GetProjectRevision() != beforeRevision+1 {
			t.Fatalf("project revision must advance by exactly one: %d -> %d", beforeRevision, response.GetProjectRevision())
		}
		// The durable row carries the same facts.
		granted, grantRevision, uninstalled := installationGrantRow(t, fixture.installationID)
		if len(granted) != 1 || granted[0] != "agent.task.run" || grantRevision != 2 || uninstalled {
			t.Fatalf("installation row wrong: granted=%v revision=%d uninstalled=%v", granted, grantRevision, uninstalled)
		}
		// One grants-updated event at sequence == new project revision, with
		// the complete eight-field payload of ADR-0003 §5.
		if events := grantsEventCount(t, fixture.projectID, beforeRevision+1); events != 1 {
			t.Fatalf("expected exactly one grants-updated event at sequence %d, got %d", beforeRevision+1, events)
		}
		payload := grantsEventPayload(t, fixture.projectID, beforeRevision+1)
		for _, field := range []string{"projectId", "revision", "installationId", "appId", "version", "manifestDigest", "grantRevision", "grantedPermissions"} {
			if _, present := payload[field]; !present {
				t.Fatalf("grants event payload missing %q: %v", field, payload)
			}
		}
		if payload["grantRevision"] != float64(2) {
			t.Fatalf("payload grantRevision wrong: %v", payload["grantRevision"])
		}
		if fmt.Sprint(payload["grantedPermissions"]) != "[agent.task.run]" {
			t.Fatalf("payload grantedPermissions wrong: %v", payload["grantedPermissions"])
		}
		if payload["installationId"] != fixture.installationID {
			t.Fatalf("payload installationId wrong: %v", payload["installationId"])
		}
		if payload["revision"] != float64(beforeRevision+1) {
			t.Fatalf("payload revision wrong: %v", payload["revision"])
		}
		// Exactly one outbox row of the same type, tied to the event's
		// occurred_at — the same-transaction proof, mirroring assertSingleFact.
		outbox := countRows(t, `SELECT count(*) FROM workos_events.outbox
			WHERE aggregate_type = 'project' AND aggregate_id = $1 AND event_type = 'project.app.grants.updated.v1'
			AND occurred_at = (SELECT occurred_at FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1 AND sequence = $2)`,
			fixture.projectID, beforeRevision+1)
		if outbox != 1 {
			t.Fatalf("expected exactly one grants-updated outbox row, got %d", outbox)
		}
		_, afterUpdated := projectRow(t, fixture.projectID)
		if !afterUpdated.After(beforeUpdated) {
			t.Fatal("real change must move the project updated_at")
		}
		// State after: [run] epoch 2, revision 3.
	})

	t.Run("SameSetNoOpKeepsFactsButConsumesKey", func(t *testing.T) {
		key := fmt.Sprintf("grants-noop-%d", time.Now().UnixNano())
		beforeRevision, beforeUpdated := projectRow(t, fixture.projectID)
		eventsBefore := countRows(t, `SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, fixture.projectID)
		outboxBefore := countRows(t, `SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'project' AND aggregate_id = $1`, fixture.projectID)

		response := mustSetGrants(t, ctx, installations, key, fixture.projectID, fixture.installationID,
			beforeRevision, []string{"agent.task.run"})
		if response.GetProjectRevision() != beforeRevision {
			t.Fatalf("no-op must not move the project revision: %d", response.GetProjectRevision())
		}
		if applied := response.GetInstallation(); applied.GetGrantRevision() != 2 {
			t.Fatalf("no-op must not move the grant revision: %d", applied.GetGrantRevision())
		}
		afterRevision, afterUpdated := projectRow(t, fixture.projectID)
		if afterRevision != beforeRevision || !afterUpdated.Equal(beforeUpdated) {
			t.Fatalf("no-op must leave the project row untouched: revision %d->%d updated %s -> %s",
				beforeRevision, afterRevision, beforeUpdated, afterUpdated)
		}
		if events := countRows(t, `SELECT count(*) FROM workos_events.events WHERE stream_type = 'project' AND stream_id = $1`, fixture.projectID); events != eventsBefore {
			t.Fatalf("no-op must not append events: %d -> %d", eventsBefore, events)
		}
		if outbox := countRows(t, `SELECT count(*) FROM workos_events.outbox WHERE aggregate_type = 'project' AND aggregate_id = $1`, fixture.projectID); outbox != outboxBefore {
			t.Fatalf("no-op must not append outbox rows: %d -> %d", outboxBefore, outbox)
		}
		// The key was still durably consumed with the no-op's result…
		command, resultGranted, resultGrantRevision, storedRevision := storedInstallationRequest(t, key)
		if command != "set-grants" || len(resultGranted) != 1 || resultGranted[0] != "agent.task.run" ||
			resultGrantRevision != 2 || storedRevision != beforeRevision {
			t.Fatalf("no-op mapping wrong: command=%s granted=%v grantRevision=%d revision=%d", command, resultGranted, resultGrantRevision, storedRevision)
		}
		// …and replays exactly, without a revision move or second fact.
		replay := mustSetGrants(t, ctx, installations, key, fixture.projectID, fixture.installationID,
			beforeRevision, []string{"agent.task.run"})
		if replay.GetProjectRevision() != beforeRevision ||
			replay.GetInstallation().GetGrantRevision() != 2 ||
			len(replay.GetInstallation().GetGrantedPermissions()) != 1 {
			t.Fatalf("no-op replay diverged: %#v", replay)
		}
		// Same key, different canonical request (different target set).
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: key, ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: beforeRevision, GrantedPermissions: []string{"agent.event.watch"},
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same-key different-set verdict: %v", err)
		}
		// State after: [run] epoch 2, revision 3.
	})

	t.Run("ReplayReturnsFirstSnapshotAfterLaterMutations", func(t *testing.T) {
		baseRevision, _ := projectRow(t, fixture.projectID)

		// First command: set [run] — a same-set no-op at this point (the
		// current grant is [run] already), still consuming its key against
		// the epoch-2 facts.
		firstKey := fmt.Sprintf("grants-replay-first-%d", time.Now().UnixNano())
		first := mustSetGrants(t, ctx, installations, firstKey, fixture.projectID, fixture.installationID,
			baseRevision, []string{"agent.task.run"})
		firstProjectRevision := first.GetProjectRevision()

		// Later real mutation: revoke all (epoch 3).
		secondKey := fmt.Sprintf("grants-replay-second-%d", time.Now().UnixNano())
		second := mustSetGrants(t, ctx, installations, secondKey, fixture.projectID, fixture.installationID,
			firstProjectRevision, nil)
		if second.GetInstallation().GetGrantRevision() != 3 || len(second.GetInstallation().GetGrantedPermissions()) != 0 {
			t.Fatalf("revoke-all wrong: %#v", second.GetInstallation())
		}

		// Replaying the first key returns the FIRST response's grant, epoch,
		// and project revision — never the mutated current row.
		replay, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: firstKey, ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: baseRevision, GrantedPermissions: []string{"agent.task.run"},
		}))
		if err != nil {
			t.Fatalf("first-key replay: %v", err)
		}
		if got := replay.Msg.GetInstallation().GetGrantedPermissions(); len(got) != 1 || got[0] != "agent.task.run" {
			t.Fatalf("replay grant diverged: %v", got)
		}
		if replay.Msg.GetInstallation().GetGrantRevision() != 2 || replay.Msg.GetProjectRevision() != firstProjectRevision {
			t.Fatalf("replay snapshot diverged: grantRevision=%d projectRevision=%d",
				replay.Msg.GetInstallation().GetGrantRevision(), replay.Msg.GetProjectRevision())
		}
		// The second key also replays its own first response (empty set,
		// epoch 3), even though the row will move on afterwards.
		secondReplay := mustSetGrants(t, ctx, installations, secondKey, fixture.projectID, fixture.installationID,
			firstProjectRevision, nil)
		if secondReplay.GetInstallation().GetGrantRevision() != 3 ||
			secondReplay.GetProjectRevision() != second.GetProjectRevision() ||
			len(secondReplay.GetInstallation().GetGrantedPermissions()) != 0 {
			t.Fatalf("second-key replay diverged: %#v", secondReplay)
		}

		// The historical install key replays the install-time facts — grant
		// [event.watch, task.run] at epoch 1, project revision 2 — not the
		// current mutated row.
		installReplay, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: fixture.installKey, ProjectId: fixture.projectID, AppId: fixture.appID,
			Version: "1.0.0", ExpectedProjectRevision: 1,
			GrantedPermissions: []string{"agent.task.run", "agent.event.watch"},
		}))
		if err != nil {
			t.Fatalf("install key replay after grant mutations: %v", err)
		}
		replayed := installReplay.Msg.GetInstallation()
		if got := replayed.GetGrantedPermissions(); len(got) != 2 || got[0] != "agent.event.watch" || got[1] != "agent.task.run" {
			t.Fatalf("install replay grant diverged: %v", got)
		}
		if replayed.GetGrantRevision() != 1 || installReplay.Msg.GetProjectRevision() != fixture.revision {
			t.Fatalf("install replay snapshot diverged: grantRevision=%d projectRevision=%d",
				replayed.GetGrantRevision(), installReplay.Msg.GetProjectRevision())
		}
		// Restore [watch, run] for the following subtests (epoch 4).
		restored := mustSetGrants(t, ctx, installations,
			fmt.Sprintf("grants-restore-%d", time.Now().UnixNano()), fixture.projectID, fixture.installationID,
			second.GetProjectRevision(), []string{"agent.task.run", "agent.event.watch"})
		if restored.GetInstallation().GetGrantRevision() != 4 {
			t.Fatalf("restore must reach epoch 4, got %d", restored.GetInstallation().GetGrantRevision())
		}
		// State after: [watch, run] epoch 4.
	})

	t.Run("FailedRequestsDoNotConsumeKey", func(t *testing.T) {
		stamp := time.Now().UnixNano()
		revision, _ := projectRow(t, fixture.projectID)
		// A non-subset grant fails after catalog resolution (PermissionDenied).
		deniedKey := fmt.Sprintf("grants-fail-denied-%d", stamp)
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: deniedKey, ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: revision, GrantedPermissions: []string{"artifact.write"},
		})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("non-subset verdict: %v", err)
		}
		// A malformed grant fails before anything else (InvalidArgument).
		malformedKey := fmt.Sprintf("grants-fail-malformed-%d", stamp)
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: malformedKey, ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: revision, GrantedPermissions: []string{"agent.task.run", "agent.task.run"},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed verdict: %v", err)
		}
		// Neither failure consumed its key: the denied key now performs a
		// real change to [] (revoke all, epoch 5)…
		denied := mustSetGrants(t, ctx, installations, deniedKey, fixture.projectID, fixture.installationID,
			revision, nil)
		if denied.GetInstallation().GetGrantRevision() != 5 || len(denied.GetInstallation().GetGrantedPermissions()) != 0 {
			t.Fatalf("denied key must drive the real change to epoch 5: %#v", denied.GetInstallation())
		}
		// …and the malformed key then succeeds as a same-set no-op at the
		// advanced revision.
		malformed := mustSetGrants(t, ctx, installations, malformedKey, fixture.projectID, fixture.installationID,
			denied.GetProjectRevision(), nil)
		if malformed.GetInstallation().GetGrantRevision() != 5 || malformed.GetProjectRevision() != denied.GetProjectRevision() {
			t.Fatalf("malformed key must succeed as a no-op: %#v", malformed)
		}
		// Restore [watch, run] for the following subtests (epoch 6).
		restored := mustSetGrants(t, ctx, installations,
			fmt.Sprintf("grants-restore2-%d", time.Now().UnixNano()), fixture.projectID, fixture.installationID,
			denied.GetProjectRevision(), []string{"agent.task.run", "agent.event.watch"})
		if restored.GetInstallation().GetGrantRevision() != 6 {
			t.Fatalf("restore must reach epoch 6, got %d", restored.GetInstallation().GetGrantRevision())
		}
		// State after: [watch, run] epoch 6.
	})

	t.Run("ErrorMatrix", func(t *testing.T) {
		revision, _ := projectRow(t, fixture.projectID)
		invalid := map[string]*appv1.SetAppGrantsRequest{
			"malformed project": {IdempotencyKey: "k", ProjectId: "not-a-uuid", InstallationId: fixture.installationID, ExpectedProjectRevision: 1},
			"malformed installation": {IdempotencyKey: "k", ProjectId: fixture.projectID,
				InstallationId: "not-a-uuid", ExpectedProjectRevision: revision},
			"malformed key": {IdempotencyKey: strings.Repeat("k", 129), ProjectId: fixture.projectID,
				InstallationId: fixture.installationID, ExpectedProjectRevision: revision},
			"zero revision": {IdempotencyKey: "k", ProjectId: fixture.projectID,
				InstallationId: fixture.installationID, ExpectedProjectRevision: 0},
			"control-character grant": {IdempotencyKey: "k", ProjectId: fixture.projectID,
				InstallationId: fixture.installationID, ExpectedProjectRevision: revision,
				GrantedPermissions: []string{"agent.task.run\x00"}},
			"duplicate grant": {IdempotencyKey: "k", ProjectId: fixture.projectID,
				InstallationId: fixture.installationID, ExpectedProjectRevision: revision,
				GrantedPermissions: []string{"agent.task.run", "agent.task.run"}},
		}
		for name, request := range invalid {
			if _, err := installations.SetAppGrants(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s must be InvalidArgument, got %v", name, err)
			}
		}
		// Non-subset target: sanitized PermissionDenied with a fixed short
		// message; no SQL, constraint, DSN, or capability input leaks.
		_, denied := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: "grants-matrix-subset", ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: revision, GrantedPermissions: []string{"artifact.read"},
		}))
		if connect.CodeOf(denied) != connect.CodePermissionDenied {
			t.Fatalf("non-subset matrix verdict: %v", denied)
		}
		text := fmt.Sprint(denied)
		for _, fragment := range []string{"SQLSTATE", "postgres://", "constraint", "artifact.read", "grant_revision", "requested set"} {
			if strings.Contains(text, fragment) {
				t.Fatalf("denial leaked %q: %v", fragment, denied)
			}
		}
		// Unknown project / unknown installation: sanitized NotFound.
		for name, request := range map[string]*appv1.SetAppGrantsRequest{
			"unknown project": {IdempotencyKey: "grants-matrix-unknown-project", ProjectId: "00000000-0000-7000-8000-000000000000",
				InstallationId: fixture.installationID, ExpectedProjectRevision: 1},
			"unknown installation": {IdempotencyKey: "grants-matrix-unknown-install", ProjectId: fixture.projectID,
				InstallationId: "00000000-0000-7000-8000-000000000000", ExpectedProjectRevision: revision},
		} {
			if _, err := installations.SetAppGrants(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("%s must be NotFound, got %v", name, err)
			}
		}
		// A foreign (other-project) installation of the same owner is
		// likewise an indistinguishable miss.
		foreign := newMutableGrantsFixture(t, ctx, "foreign")
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: "grants-matrix-foreign-install", ProjectId: fixture.projectID,
			InstallationId: foreign.installationID, ExpectedProjectRevision: revision,
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("foreign installation must be NotFound, got %v", err)
		}
		// An uninstalled installation is a sanitized miss too.
		uninstalled, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: fmt.Sprintf("grants-matrix-uninstall-%d", time.Now().UnixNano()),
			ProjectId:      foreign.projectID, InstallationId: foreign.installationID,
			ExpectedProjectRevision: foreign.revision,
		}))
		if err != nil {
			t.Fatalf("uninstall foreign fixture: %v", err)
		}
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: "grants-matrix-uninstalled", ProjectId: foreign.projectID,
			InstallationId: foreign.installationID, ExpectedProjectRevision: uninstalled.Msg.GetProjectRevision(),
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("uninstalled installation must be NotFound, got %v", err)
		}
		// Stale expected revision: Aborted.
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: "grants-matrix-stale", ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: 1, GrantedPermissions: []string{"agent.task.run"},
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("stale revision must be Aborted, got %v", err)
		}
	})

	t.Run("SameKeyAcrossCommandsAndProjectsAborts", func(t *testing.T) {
		revision, _ := projectRow(t, fixture.projectID)
		// Consume one key with a valid Set…
		sharedKey := fmt.Sprintf("grants-shared-%d", time.Now().UnixNano())
		mustSetGrants(t, ctx, installations, sharedKey, fixture.projectID, fixture.installationID,
			revision, []string{"agent.task.run", "agent.event.watch"})
		// …then reuse it for a different canonical Set (different target set).
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: sharedKey, ProjectId: fixture.projectID, InstallationId: fixture.installationID,
			ExpectedProjectRevision: revision, GrantedPermissions: []string{"agent.task.run"},
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same-key different-set must Abort: %v", err)
		}
		// Reuse across commands: the set-grants key as an install request is
		// a different canonical digest regardless of the app's existence.
		if _, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey: sharedKey, ProjectId: fixture.projectID, AppId: "grants-shared-app",
			ExpectedProjectRevision: revision,
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("set key reused by install must Abort: %v", err)
		}
		// Reuse across projects: another project's installation under the
		// same key is a different canonical request.
		other := newMutableGrantsFixture(t, ctx, "cross")
		if _, err := installations.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: sharedKey, ProjectId: other.projectID, InstallationId: other.installationID,
			ExpectedProjectRevision: other.revision, GrantedPermissions: []string{"agent.task.run"},
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("set key reused across projects must Abort: %v", err)
		}
	})
}

// TestMutableProjectAppGrantsConcurrency proves the database arbitrates
// concurrent grant mutations on the same expected revision: exactly one
// winner, sanitized Aborted losers, one grant epoch bump, one event — and
// that Set racing Uninstall on the same revision leaves one consistent fact.
func TestMutableProjectAppGrantsConcurrency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	fixture := newMutableGrantsFixture(t, ctx, "race")

	t.Run("SameExpectedRevisionYieldsExactlyOneWinner", func(t *testing.T) {
		stamp := time.Now().UnixNano()
		revision, _ := projectRow(t, fixture.projectID)
		eventsBefore := countRows(t, `SELECT count(*) FROM workos_events.events
			WHERE stream_type = 'project' AND stream_id = $1 AND event_type = 'project.app.grants.updated.v1'`, fixture.projectID)

		const concurrency = 4
		start := make(chan struct{})
		outcomes := make(chan error, concurrency)
		var group sync.WaitGroup
		for index := 0; index < concurrency; index++ {
			// Independent HTTP clients: real concurrent gateway callers, not
			// a serialized client-side queue.
			client := installationClients(t)
			target := []string{"agent.task.run"}
			if index%2 == 1 {
				target = []string{"agent.event.watch"}
			}
			group.Add(1)
			go func(client appv1connect.AppInstallationServiceClient, target []string, index int) {
				defer group.Done()
				<-start
				_, err := client.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
					IdempotencyKey:          fmt.Sprintf("grants-race-%d-%d", stamp, index),
					ProjectId:               fixture.projectID,
					InstallationId:          fixture.installationID,
					ExpectedProjectRevision: revision,
					GrantedPermissions:      target,
				}))
				outcomes <- err
			}(client, target, index)
		}
		close(start)
		group.Wait()
		close(outcomes)
		winners, aborted := 0, 0
		for err := range outcomes {
			if err == nil {
				winners++
				continue
			}
			if connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("unexpected concurrent verdict: %v", err)
			}
			aborted++
		}
		if winners != 1 || aborted != concurrency-1 {
			t.Fatalf("exactly one winner expected: winners=%d aborted=%d", winners, aborted)
		}
		// Exactly one grant epoch bump, one project revision bump, one event.
		_, grantRevision, _ := installationGrantRow(t, fixture.installationID)
		if grantRevision != 2 {
			t.Fatalf("grant revision must be exactly 2 after the race, got %d", grantRevision)
		}
		if events := countRows(t, `SELECT count(*) FROM workos_events.events
			WHERE stream_type = 'project' AND stream_id = $1 AND event_type = 'project.app.grants.updated.v1'`, fixture.projectID); events != eventsBefore+1 {
			t.Fatalf("race must append exactly one grants event: %d -> %d", eventsBefore, events)
		}
		if current, _ := projectRow(t, fixture.projectID); current != revision+1 {
			t.Fatalf("project revision must advance exactly once: %d -> %d", revision, current)
		}
	})

	t.Run("SetCompetesWithUninstallOnSameRevision", func(t *testing.T) {
		rival := newMutableGrantsFixture(t, ctx, "uninstall-race")
		setter, uninstaller := installationClients(t), installationClients(t)
		start := make(chan struct{})
		outcomes := make(chan error, 2)
		var group sync.WaitGroup
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := setter.SetAppGrants(ctx, connect.NewRequest(&appv1.SetAppGrantsRequest{
				IdempotencyKey:          fmt.Sprintf("grants-uninstall-race-set-%d", time.Now().UnixNano()),
				ProjectId:               rival.projectID,
				InstallationId:          rival.installationID,
				ExpectedProjectRevision: rival.revision,
				GrantedPermissions:      []string{"agent.task.run"},
			}))
			outcomes <- err
		}()
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := uninstaller.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
				IdempotencyKey:          fmt.Sprintf("grants-uninstall-race-uninstall-%d", time.Now().UnixNano()),
				ProjectId:               rival.projectID,
				InstallationId:          rival.installationID,
				ExpectedProjectRevision: rival.revision,
			}))
			outcomes <- err
		}()
		close(start)
		group.Wait()
		close(outcomes)
		first, second := <-outcomes, <-outcomes
		if first == nil && second == nil {
			t.Fatal("set and uninstall on the same expected revision cannot both win")
		}
		if first != nil && second != nil {
			t.Fatalf("one of set/uninstall must win: %v / %v", first, second)
		}
		// The database holds one consistent outcome: either an active
		// installation at epoch 2 with grant [run], or a tombstone at epoch 1.
		granted, grantRevision, uninstalled := installationGrantRow(t, rival.installationID)
		if uninstalled {
			if grantRevision != 1 {
				t.Fatalf("uninstall winner must leave epoch 1, got %d", grantRevision)
			}
		} else if grantRevision != 2 || len(granted) != 1 || granted[0] != "agent.task.run" {
			t.Fatalf("set winner must persist grant [run] at epoch 2: %v %d", granted, grantRevision)
		}
	})
}
