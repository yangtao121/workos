//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/orchestration"
	orchestrationtransport "github.com/yangtao121/workos/internal/core/orchestration/transport"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// TestMutableGrantRevocationChain proves the immediate-revocation semantics
// of ADR-0003 §3/§4 through the real gateway, Core, runtime-host, and Fake
// Harness, numbered after the prompt's "Runtime / revocation integration"
// list. Item 1 (run/watch work before the change) is the setup; item 3
// (already-open watch stream termination) is deterministic only without a
// harness worker, so it lives in TestGrantEpochWatchStreamTerminates below on
// a scratch database; item 9 (foreign owner/device/token, closed/expired
// session, uninstalled app) stays covered by the existing matrix:
// TestAppBridgeVerticalSlice (forged/missing token, empty-grant capability
// isolation), TestAppBridgeRevocationAndUserVisibility (uninstalled app
// denial with durable tasks), and TestSurfaceCloseClearsBridgeTokenInStorage
// plus TestWebBundleSurfaceVerticalSlice (closed sessions fail closed).
func TestMutableGrantRevocationChain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	installations := installationClients(t)
	surfaces := surfaceClients(t)
	bridge := bridgeClients(t)
	agentTasks := agentTaskClients(t)
	stamp := time.Now().UnixNano()
	fixture := newMutableGrantsFixture(t, ctx, "revoke")

	// (1) Open the surface under the initial grant and drive one real task
	// through the Fake Harness to its terminal event with the old token.
	openSurface := func(key string) (*surfacev1.SurfaceSession, string) {
		t.Helper()
		response, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: key, AppInstanceId: fixture.installationID, ProjectId: fixture.projectID,
			DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		}))
		if err != nil {
			t.Fatalf("create surface (%s): %v", key, err)
		}
		session := response.Msg.GetSession()
		if session.GetBridgeToken() == "" {
			t.Fatalf("open session must carry a bridge token: %#v", session)
		}
		return session, session.GetBridgeToken()
	}
	oldSession, oldToken := openSurface(fmt.Sprintf("grants-surface-old-%d", stamp))

	runTask := func(token, key, goal string) (string, error) {
		response, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
			IdempotencyKey: key, Goal: goal,
		}, token))
		if err != nil {
			return "", err
		}
		return response.Msg.GetTaskId(), nil
	}
	watchToTerminal := func(token, taskID string) error {
		stream, err := bridge.WatchAgentTaskEvents(ctx, watchRequest(taskID, 0, token))
		if err != nil {
			return err
		}
		defer stream.Close()
		for stream.Receive() {
			if stream.Msg().GetEvent().GetRunCompleted() != nil {
				return nil
			}
		}
		if err := stream.Err(); err != nil {
			return err
		}
		return errors.New("stream ended without a terminal event")
	}

	firstTask, err := runTask(oldToken, fmt.Sprintf("grants-run-before-%d", stamp), "goal before revocation")
	if err != nil {
		t.Fatalf("pre-revocation run failed: %v", err)
	}
	if err := watchToTerminal(oldToken, firstTask); err != nil {
		t.Fatalf("pre-revocation watch failed: %v", err)
	}

	// (2) Set the grant to a strict subset that still contains watch — a real
	// change (epoch 2). After the transaction returns, every bridge method of
	// the OLD session is denied server-side, including the capability the new
	// grant still carries: the denial is the Core epoch check, not UI hiding.
	subset := mustSetGrants(t, ctx, installations,
		fmt.Sprintf("grants-set-subset-%d", stamp), fixture.projectID, fixture.installationID,
		subsetRevision(t, fixture.projectID), []string{"agent.event.watch"})
	if subset.GetInstallation().GetGrantRevision() != 2 {
		t.Fatalf("subset set must reach epoch 2: %#v", subset.GetInstallation())
	}
	if _, err := runTask(oldToken, fmt.Sprintf("grants-run-after-%d", stamp), "goal after revocation"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("old-token run after revocation must be PermissionDenied, got %v", err)
	}
	if err := watchToTerminal(oldToken, firstTask); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("old-token watch after revocation must be PermissionDenied, got %v", err)
	}

	// (4) Replaying the old CreateSurface key fails closed: the freshly
	// resolved epoch no longer equals the persisted one, so no new token is
	// minted for the superseded epoch and no second session row appears.
	replayRequest := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: fmt.Sprintf("grants-surface-old-%d", stamp), AppInstanceId: fixture.installationID,
		ProjectId: fixture.projectID, DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport: &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	})
	_, replayErr := surfaces.CreateSurface(ctx, replayRequest)
	if connect.CodeOf(replayErr) != connect.CodeFailedPrecondition {
		t.Fatalf("old create-key replay after grant change must be FailedPrecondition, got %v", replayErr)
	}
	if message := fmt.Sprint(replayErr); strings.ContainsAny(message, "0123456789\"'") {
		t.Fatalf("replay verdict leaked internals: %v", replayErr)
	}
	if sessions := countRows(t,
		`SELECT count(*) FROM workos_runtime.surface_sessions WHERE app_instance_id = $1`, fixture.installationID); sessions != 1 {
		t.Fatalf("failed replay must not persist a second session, got %d", sessions)
	}

	// (5) A fresh key snapshots the new epoch: effective capabilities are
	// exactly new grant ∩ implemented methods, persisted as the session's
	// grant revision column.
	freshSession, freshToken := openSurface(fmt.Sprintf("grants-surface-fresh-%d", stamp))
	if got := freshSession.GetBridgeCapabilities(); len(got) != 1 || got[0] != "agent.event.watch" {
		t.Fatalf("fresh session capabilities must be exactly [agent.event.watch], got %v", got)
	}
	if revision := sessionGrantRevision(t, freshSession.GetId()); revision != 2 {
		t.Fatalf("fresh session must persist grant revision 2, got %d", revision)
	}
	if revision := sessionGrantRevision(t, oldSession.GetId()); revision != 1 {
		t.Fatalf("old session must keep its persisted epoch 1, got %d", revision)
	}
	// The fresh session can use the granted watch on the earlier task…
	if err := watchToTerminal(freshToken, firstTask); err != nil {
		t.Fatalf("fresh-session watch of the durable task failed: %v", err)
	}
	// …but not the ungranted run (the local capability gate itself denies it).
	if _, err := runTask(freshToken, fmt.Sprintf("grants-run-fresh-epoch2-%d", stamp), "goal on partial grant"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("fresh-session run on a grant without run must be PermissionDenied, got %v", err)
	}

	// (6) Re-grant both capabilities (epoch 3): old sessions — even the epoch-2
	// fresh one — cannot use the newly added capability automatically; only a
	// newly created surface carries the new epoch.
	regranted := mustSetGrants(t, ctx, installations,
		fmt.Sprintf("grants-set-regrant-%d", stamp), fixture.projectID, fixture.installationID,
		subset.GetProjectRevision(), []string{"agent.task.run", "agent.event.watch"})
	if regranted.GetInstallation().GetGrantRevision() != 3 {
		t.Fatalf("re-grant must reach epoch 3: %#v", regranted.GetInstallation())
	}
	for name, token := range map[string]string{"epoch-1 session": oldToken, "epoch-2 session": freshToken} {
		if _, err := runTask(token, fmt.Sprintf("grants-run-stale-%s-%d", name, stamp), "goal on stale epoch"); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s run after re-grant must be PermissionDenied (epoch mismatch), got %v", name, err)
		}
		if err := watchToTerminal(token, firstTask); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s watch after re-grant must be PermissionDenied (epoch mismatch), got %v", name, err)
		}
	}
	newestSession, newestToken := openSurface(fmt.Sprintf("grants-surface-newest-%d", stamp))
	if got := newestSession.GetBridgeCapabilities(); len(got) != 2 || got[0] != "agent.event.watch" || got[1] != "agent.task.run" {
		t.Fatalf("newest session capabilities wrong: %v", got)
	}
	if revision := sessionGrantRevision(t, newestSession.GetId()); revision != 3 {
		t.Fatalf("newest session must persist grant revision 3, got %d", revision)
	}
	secondTask, err := runTask(newestToken, fmt.Sprintf("grants-run-newest-%d", stamp), "goal on new epoch")
	if err != nil {
		t.Fatalf("newest-session run failed: %v", err)
	}
	if err := watchToTerminal(newestToken, secondTask); err != nil {
		t.Fatalf("newest-session watch failed: %v", err)
	}

	// (7) The durable pre-revocation task was never implicitly cancelled: it
	// stays completed and owner-visible through all grant churn, while only
	// new bridge authorization reads fail.
	final, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: firstTask}))
	if err != nil || final.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("durable task must survive revocation as completed: %v %#v", err, final.Msg.GetTask())
	}

	// (8) A grant mutation is not an uninstall: the static Web Bundle assets
	// of the still-active installation keep serving for the open session.
	if response := httpGet(t, oldSession.GetUrl()); response.StatusCode != http.StatusOK {
		t.Fatalf("static assets must keep serving while the installation is active, got %d", response.StatusCode)
	}
	if _, _, uninstalled := installationGrantRow(t, fixture.installationID); uninstalled {
		t.Fatal("grant mutation must not tombstone the installation")
	}
}

// subsetRevision reads the current project revision for the next Set command.
func subsetRevision(t *testing.T, projectID string) int64 {
	t.Helper()
	revision, _ := projectRow(t, projectID)
	return revision
}

// fixedPinnedCatalog satisfies the Project application's neutral AppCatalog
// port with immutable registry facts, so the scratch-database test below can
// run the real install/SetAppGrants adjudication without the registry stack.
type fixedPinnedCatalog struct {
	pinned projectdomain.PinnedApp
}

func (c *fixedPinnedCatalog) Resolve(context.Context, string, string, string) (projectdomain.PinnedApp, error) {
	return c.pinned, nil
}

// TestGrantEpochWatchStreamTerminates proves prompt item 3 deterministically:
// a watch stream that passed Core authorization before a real SetAppGrants
// commit terminates on the next polling reauthorization round (bounded by the
// 200 ms Core ticker through a channel deadline — no arbitrary sleep), ends
// with the sanitized stale-epoch PermissionDenied, forwards no further
// events, and never cancels the durable task. The scratch database has no
// harness worker attached, so the watched task stays queued forever — the
// only way this stream can end is the epoch check. The chain is the real
// Project/Agent PostgreSQL adapters, the real SetAppGrants and AppAgent
// application services, and the real private Core transport over HTTP.
func TestGrantEpochWatchStreamTerminates(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	owner, project := newUUIDForTest(401), newUUIDForTest(402)
	// agent_tasks references users and projects; seed both rows the way the
	// acceptance volume bootstrap does (mirrors TestAppTaskRepositoryConcurrency).
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Grant Epoch Owner', now()) ON CONFLICT DO NOTHING",
		owner,
	); err != nil {
		t.Fatalf("seed owner user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1, $2, 'grant-epoch-project', 'Grant Epoch', $3, $4, now(), now())",
		project, owner, newUUIDForTest(403), newUUIDForTest(404),
	); err != nil {
		t.Fatalf("seed project row: %v", err)
	}

	catalog := &fixedPinnedCatalog{pinned: projectdomain.PinnedApp{
		AppID: "grant-epoch-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("7", 64),
		Scope:          "user",
		Permissions:    []string{"agent.event.watch", "agent.task.run"},
	}}
	installationService, err := projectapp.NewInstallationService(projectpostgres.New(pool), catalog, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installationService.Install(ctx, projectapp.InstallInput{
		OwnerUserID: owner, IdempotencyKey: "grant-epoch-install", ProjectID: project,
		AppID: "grant-epoch-app", Version: "1.0.0", ExpectedRevision: 1,
		GrantedPermissions: []string{"agent.task.run", "agent.event.watch"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if installed.Installation.GrantRevision != 1 {
		t.Fatalf("installation must start at epoch 1: %#v", installed.Installation)
	}

	// The real private Core App Agent service over the real Agent store; no
	// harness worker polls this scratch database, so submitted tasks stay
	// queued (non-terminal) indefinitely.
	router, err := orchestration.NewTaskRouter(
		agentapp.New(agentpostgres.New(pool), ids.UUIDv7{}), projectapp.New(projectpostgres.New(pool), ids.UUIDv7{}),
		staticDefaultPolicies{}, staticFullCapabilities{}, "fake",
	)
	if err != nil {
		t.Fatal(err)
	}
	appAgent, err := orchestration.NewAppAgentService(projectpostgres.New(pool), router)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := agentv1connect.NewAppAgentServiceHandler(orchestrationtransport.NewAppAgent(appAgent))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentv1connect.NewAppAgentServiceClient(server.Client(), server.URL)

	newRun := func(key string, epoch int64) *connect.Request[agentv1.RunAgentTaskRequest] {
		request := connect.NewRequest(&agentv1.RunAgentTaskRequest{
			ProjectId: project, AppInstanceId: installed.Installation.ID,
			ClientIdempotencyKey: key, Goal: "grant epoch watch goal",
			InstallationGrantRevision: epoch,
		})
		request.Header().Set(identity.UserHeader, owner)
		request.Header().Set(identity.DeviceHeader, newUUIDForTest(405))
		return request
	}

	// A run authorized at epoch 1 creates the durable (forever-queued) task.
	run, err := client.RunAgentTask(ctx, newRun("grant-epoch-run-1", 1))
	if err != nil {
		t.Fatalf("authorized run: %v", err)
	}
	taskID := run.Msg.GetTaskId()

	// Open the watch stream at epoch 1 before any grant change. Whatever the
	// interleaving, the stream can only end through the epoch check: the task
	// never reaches a terminal state in this database.
	watchDone := make(chan error, 1)
	go func() {
		request := connect.NewRequest(&agentv1.WatchAgentTaskEventsRequest{
			ProjectId: project, AppInstanceId: installed.Installation.ID,
			TaskId: taskID, AfterSequence: 0, InstallationGrantRevision: 1,
		})
		request.Header().Set(identity.UserHeader, owner)
		request.Header().Set(identity.DeviceHeader, newUUIDForTest(406))
		stream, err := client.WatchAgentTaskEvents(ctx, request)
		if err != nil {
			watchDone <- err
			return
		}
		for stream.Receive() {
			// Any event forwarded after the revocation would prove the old
			// epoch still streams; record it as a failure.
			watchDone <- fmt.Errorf("stale epoch received event %d", stream.Msg().GetEvent().GetSequence())
			return
		}
		watchDone <- stream.Err()
	}()

	// Commit the real grant mutation (epoch 1 -> 2, grant shrinks to [run]).
	set, err := installationService.SetAppGrants(ctx, projectapp.SetAppGrantsInput{
		OwnerUserID: owner, IdempotencyKey: "grant-epoch-set", ProjectID: project,
		InstallationID: installed.Installation.ID, ExpectedRevision: installed.ProjectRevision,
		GrantedPermissions: []string{"agent.task.run"},
	})
	if err != nil {
		t.Fatalf("set grants: %v", err)
	}
	if set.Installation.GrantRevision != 2 {
		t.Fatalf("real change must reach epoch 2: %#v", set.Installation)
	}

	// The open stream terminates on the next reauthorization round: bounded
	// by the Core ticker through a deadline, never an arbitrary sleep.
	select {
	case err := <-watchDone:
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("open watch stream must end with the sanitized stale-epoch PermissionDenied, got %v", err)
		}
		if message := fmt.Sprint(err); strings.ContainsAny(message, "0123456789") {
			t.Fatalf("stale-epoch verdict leaked internals: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch stream did not terminate after the grant mutation")
	}

	// New authorization reads after the commit all fail closed at the epoch
	// check: a stale session revision and an absent (<= 0) one are the same
	// indistinguishable verdict, while the fresh epoch still works for the
	// capabilities the new grant carries.
	if _, err := client.RunAgentTask(ctx, newRun("grant-epoch-run-stale", 1)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("run with a stale epoch must be PermissionDenied, got %v", err)
	}
	if _, err := client.RunAgentTask(ctx, newRun("grant-epoch-run-absent", 0)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("run with an absent epoch must be PermissionDenied, got %v", err)
	}
	freshRun, err := client.RunAgentTask(ctx, newRun("grant-epoch-run-2", 2))
	if err != nil {
		t.Fatalf("run at the fresh epoch failed: %v", err)
	}
	if freshRun.Msg.GetTaskId() == "" {
		t.Fatal("fresh-epoch run created no task")
	}
	// The new grant dropped watch: an epoch-2 watch is denied on the grant
	// itself — proving the epoch comparison precedes grant validation only
	// in order, not in leak (fixed short messages either way). Streaming
	// verdicts arrive lazily, so drain until the server's decision.
	watchRequest := connect.NewRequest(&agentv1.WatchAgentTaskEventsRequest{
		ProjectId: project, AppInstanceId: installed.Installation.ID,
		TaskId: taskID, AfterSequence: 0, InstallationGrantRevision: 2,
	})
	watchRequest.Header().Set(identity.UserHeader, owner)
	watchRequest.Header().Set(identity.DeviceHeader, newUUIDForTest(407))
	watchStream, watchErr := client.WatchAgentTaskEvents(ctx, watchRequest)
	if watchErr == nil {
		for watchStream.Receive() {
		}
		watchErr = watchStream.Err()
	}
	if connect.CodeOf(watchErr) != connect.CodePermissionDenied {
		t.Fatalf("epoch-2 watch without the watch grant must be PermissionDenied, got %v", watchErr)
	}

	// The durable task was not implicitly cancelled by the revocation.
	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM workos_core.agent_tasks WHERE id = $1`, taskID,
	).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("revoked-epoch task must stay queued (durable, not cancelled): %q %v", state, err)
	}
}
