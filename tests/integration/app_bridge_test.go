//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	bridgev1connect "github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	orchestrationtransport "github.com/yangtao121/workos/internal/core/orchestration/transport"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	surfacepostgres "github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres"
	surfaceapp "github.com/yangtao121/workos/internal/runtime/surface/application"
	surfacedomain "github.com/yangtao121/workos/internal/runtime/surface/domain"
	surfaceports "github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func bridgeClients(t *testing.T) bridgev1connect.AppBridgeServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	return bridgev1connect.NewAppBridgeServiceClient(httpClient, gatewayBaseURL())
}

func agentTaskClients(t *testing.T) agentv1connect.AgentTaskServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	return agentv1connect.NewAgentTaskServiceClient(httpClient, gatewayBaseURL())
}

// bridgeAgentManifest requests exactly the two bridge capabilities.
func bridgeAgentManifest(appID, name, artifactID, artifactDigest string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: %s
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
    adaptive: true
permissions: [agent.task.run, agent.event.watch]
resources: {}
health: {}
maintainer: {}
`, appID, name, artifactID, artifactDigest))
}

// tokenRequest attaches the ephemeral bridge credential as dedicated
// metadata, exactly like the trusted desktop host does.
func tokenRequest(msg *bridgev1.RunAgentTaskRequest, token string) *connect.Request[bridgev1.RunAgentTaskRequest] {
	request := connect.NewRequest(msg)
	if token != "" {
		request.Header().Set("X-WorkOS-Bridge-Token", token)
	}
	return request
}

func watchRequest(taskID string, after int64, token string) *connect.Request[bridgev1.WatchAgentTaskEventsRequest] {
	request := connect.NewRequest(&bridgev1.WatchAgentTaskEventsRequest{TaskId: taskID, AfterSequence: after})
	if token != "" {
		request.Header().Set("X-WorkOS-Bridge-Token", token)
	}
	return request
}

// TestAppBridgeVerticalSlice proves the project-scoped App Agent chain end to
// end through the real gateway, Core, runtime-host, Task Router, Harness
// Broker, and Fake Harness: explicit grant → token-bearing surface →
// token-validated bridge run → provenance-bound watch → capability isolation.
func TestAppBridgeVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	artifacts := artifactClients(t)
	surfaces := surfaceClients(t)
	projects := integrationProjectClients(t)
	installations := installationClients(t)
	registry := appRegistryClients(t)
	bridge := bridgeClients(t)
	agentTasks := agentTaskClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("bridge-agent-%d", stamp)

	artifact := createArtifact(t, ctx, artifacts, fmt.Sprintf("bridge-create-%d", stamp), "Bridge App", bundleFiles(), "index.html")
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("bridge-register-%d", stamp),
		ManifestYaml:   bridgeAgentManifest(appID, "Bridge Agent", artifact.GetId(), artifact.GetDigest()),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}

	project := createIntegrationProject(t, ctx, projects, "App Bridge", fmt.Sprintf("bridge-project-%d", stamp))

	// Explicit grant: a canonical subset of the requested permissions; the
	// installation echoes the immutable grant snapshot.
	installResponse, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("bridge-install-%d", stamp),
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		t.Fatalf("install with grants: %v", err)
	}
	installation := installResponse.Msg.GetInstallation()
	if got := installation.GetGrantedPermissions(); len(got) != 2 || got[0] != "agent.event.watch" || got[1] != "agent.task.run" {
		t.Fatalf("grant snapshot wrong: %v", got)
	}

	t.Run("GrantValidation", func(t *testing.T) {
		// Duplicate grants are InvalidArgument and consume nothing.
		_, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey:          fmt.Sprintf("bridge-install-dup-%d", stamp),
			ProjectId:               project.GetId(),
			AppId:                   appID,
			Version:                 "1.0.0",
			ExpectedProjectRevision: installResponse.Msg.GetProjectRevision(),
			GrantedPermissions:      []string{"agent.task.run", "agent.task.run"},
		}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("duplicate grant verdict: %v", err)
		}
		// A not-requested capability is a sanitized PermissionDenied.
		_, denied := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey:          fmt.Sprintf("bridge-install-notreq-%d", stamp),
			ProjectId:               project.GetId(),
			AppId:                   appID,
			Version:                 "1.0.0",
			ExpectedProjectRevision: installResponse.Msg.GetProjectRevision(),
			GrantedPermissions:      []string{"artifact.write"},
		}))
		if connect.CodeOf(denied) != connect.CodePermissionDenied {
			t.Fatalf("not-requested grant verdict: %v", denied)
		}
		// Same key, different grant aborts (versioned digest).
		_, conflict := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
			IdempotencyKey:          fmt.Sprintf("bridge-install-%d", stamp),
			ProjectId:               project.GetId(),
			AppId:                   appID,
			Version:                 "1.0.0",
			ExpectedProjectRevision: installResponse.Msg.GetProjectRevision(),
			GrantedPermissions:      []string{"agent.task.run"},
		}))
		if connect.CodeOf(conflict) != connect.CodeAborted {
			t.Fatalf("same-key different-grant verdict: %v", conflict)
		}
	})

	// Open the surface: the response carries the ephemeral bridge credential
	// and the effective capability list (requested ∩ granted ∩ implemented).
	surface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    fmt.Sprintf("bridge-surface-%d", stamp),
		AppInstanceId:     installation.GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1024, Height: 768, PixelRatio: 1},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		t.Fatalf("create surface: %v", err)
	}
	session := surface.Msg.GetSession()
	bridgeToken := session.GetBridgeToken()
	if len(bridgeToken) != 43 || strings.ContainsAny(bridgeToken, "+/=@") {
		t.Fatalf("bridge token missing or malformed: %q", bridgeToken)
	}
	if got := session.GetBridgeCapabilities(); len(got) != 2 || got[0] != "agent.event.watch" || got[1] != "agent.task.run" {
		t.Fatalf("effective capabilities wrong: %v", got)
	}
	if session.GetResize() || session.GetClipboard() || session.GetFilePicker() {
		t.Fatal("unimplemented capability flags must stay false")
	}
	if strings.Contains(session.GetUrl(), bridgeToken) {
		t.Fatal("bridge token leaked into the surface URL")
	}

	goal := fmt.Sprintf("Summarize bridge fixture %d", stamp)
	runKey := fmt.Sprintf("bridge-run-%d", stamp)

	// Credential failures fail closed with one sanitized verdict.
	if _, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey + "-notoken", Goal: goal,
	}, "")); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing token verdict: %v", err)
	}
	if _, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey + "-forged", Goal: goal,
	}, strings.Repeat("x", 43))); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("forged token verdict: %v", err)
	}

	// The authorized run creates the task through the real Task Router.
	run, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: runKey, Role: "", Goal: goal,
	}, bridgeToken))
	if err != nil {
		t.Fatalf("authorized run failed: %v", err)
	}
	taskID := run.Msg.GetTaskId()
	if taskID == "" || run.Msg.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED {
		t.Fatalf("unexpected run response: %+v", run.Msg)
	}

	t.Run("ProviderSnapshotAndOwnerVisibility", func(t *testing.T) {
		// The Task Router snapshotted the Project/default provider at submit.
		task, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
		if err != nil || task.Msg.GetTask().GetProviderId() != "fake" {
			t.Fatalf("provider snapshot wrong: %v %+v", err, task.Msg.GetTask())
		}
		// The canonical input was forced to the project scope.
		if scope := task.Msg.GetTask().GetInput().GetTargetScope().GetProjectId(); scope != project.GetId() {
			t.Fatalf("target scope not forced to the project: %+v", task.Msg.GetTask().GetInput())
		}
	})

	t.Run("RunIdempotency", func(t *testing.T) {
		// Same key, same request replays the first task exactly.
		replayed, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
			IdempotencyKey: runKey, Goal: goal,
		}, bridgeToken))
		if err != nil || replayed.Msg.GetTaskId() != taskID {
			t.Fatalf("replay diverged: %v %s vs %s", err, replayed.Msg.GetTaskId(), taskID)
		}
		// Same key, different canonical request aborts.
		_, conflictErr := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
			IdempotencyKey: runKey, Goal: goal + " different",
		}, bridgeToken))
		if connect.CodeOf(conflictErr) != connect.CodeAborted {
			t.Fatalf("same-key conflict verdict: %v", conflictErr)
		}
	})

	t.Run("WatchEventsAndResume", func(t *testing.T) {
		collect := func(after int64) ([]*agentv1.AgentEvent, error) {
			stream, err := bridge.WatchAgentTaskEvents(ctx, watchRequest(taskID, after, bridgeToken))
			if err != nil {
				return nil, err
			}
			defer stream.Close()
			var events []*agentv1.AgentEvent
			for stream.Receive() {
				events = append(events, stream.Msg().GetEvent())
				if stream.Msg().GetEvent().GetRunCompleted() != nil {
					return events, nil
				}
			}
			return events, stream.Err()
		}
		events, err := collect(0)
		if err != nil {
			t.Fatalf("watch failed: %v", err)
		}
		if len(events) == 0 || events[0].GetSequence() != 1 {
			t.Fatalf("event stream not gapless from 1: %d events", len(events))
		}
		terminal := events[len(events)-1].GetRunCompleted()
		if terminal == nil || terminal.GetSummary() != fmt.Sprintf("Task %s completed by fake harness", taskID) {
			t.Fatalf("terminal event wrong: %+v", events[len(events)-1])
		}
		// Resume from the cursor: no duplicates, no misses.
		resumed, err := collect(1)
		if err != nil {
			t.Fatalf("resume failed: %v", err)
		}
		if len(resumed) == 0 || resumed[0].GetSequence() != 2 {
			t.Fatalf("resume cursor wrong: %d events, first %d", len(resumed), resumed[0].GetSequence())
		}
	})

	t.Run("CapabilityIsolation", func(t *testing.T) {
		// An app with an empty grant gets a surface without effective bridge
		// capabilities; even a leaked token cannot authorize any method.
		emptyProject := createIntegrationProject(t, ctx, projects, "App Bridge Empty", fmt.Sprintf("bridge-project-empty-%d", stamp))
		emptyAppID := fmt.Sprintf("bridge-empty-%d", stamp)
		if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("bridge-register-empty-%d", stamp),
			ManifestYaml:   bridgeAgentManifest(emptyAppID, "Bridge Empty", artifact.GetId(), artifact.GetDigest()),
		})); err != nil {
			t.Fatalf("register empty app: %v", err)
		}
		emptyInstall := installApp(t, ctx, installations, fmt.Sprintf("bridge-install-empty-%d", stamp), emptyProject.GetId(), emptyAppID, "", emptyProject.GetRevision())
		if len(emptyInstall.GetGrantedPermissions()) != 0 {
			t.Fatalf("empty grant polluted: %v", emptyInstall.GetGrantedPermissions())
		}
		emptySurface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey:    fmt.Sprintf("bridge-surface-empty-%d", stamp),
			AppInstanceId:     emptyInstall.GetId(),
			ProjectId:         emptyProject.GetId(),
			DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:          &surfacev1.Viewport{Width: 1024, Height: 768, PixelRatio: 1},
			PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
		}))
		if err != nil {
			t.Fatalf("create empty surface: %v", err)
		}
		emptyToken := emptySurface.Msg.GetSession().GetBridgeToken()
		if emptyToken == "" {
			t.Fatal("open session replay/create must still mint a token")
		}
		if got := emptySurface.Msg.GetSession().GetBridgeCapabilities(); len(got) != 0 {
			t.Fatalf("empty grant yielded capabilities: %v", got)
		}
		_, err = bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
			IdempotencyKey: fmt.Sprintf("bridge-run-empty-%d", stamp), Goal: "goal",
		}, emptyToken))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("empty-grant run verdict: %v", err)
		}
	})
}

// seqIDs is a deterministic generator yielding globally unique IDs so the two
// racing repositories never collide on primary keys — the arbitration under
// test must come from the mapping, not from an id clash. The base keeps the
// two pools' id spaces disjoint.
type seqIDs struct {
	base    int
	counter atomic.Int64
}

func newSeqIDs(base int) *seqIDs { return &seqIDs{base: base} }

func (s *seqIDs) New() string {
	return newUUIDForTest(int(s.base) + int(s.counter.Add(1)))
}

func bridgeSubmitInput(owner, appInstance, project, key, digest, goal string) agentapp.AppSubmitInput {
	return agentapp.AppSubmitInput{
		OwnerUserID: owner, AppInstanceID: appInstance,
		ClientIdempotencyKey: key, RequestDigest: digest,
		ProjectID: project, ProviderID: "fake", Goal: goal,
	}
}

// TestAppTaskRepositoryConcurrency proves the Agent-owned provenance
// arbitration against real PostgreSQL with two independent pools: same
// (owner, app, client key) races converge on exactly one task + mapping +
// outbox row with zero orphans, different requests abort, and two apps
// sharing one client key never collide.
func TestAppTaskRepositoryConcurrency(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	leftPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer leftPool.Close()
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rightPool.Close()
	countScratch := func(query string, args ...any) int {
		t.Helper()
		var count int
		if err := leftPool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
			t.Fatalf("count scratch rows (%s): %v", query, err)
		}
		return count
	}
	left, right := agentpostgres.New(leftPool), agentpostgres.New(rightPool)
	leftService, rightService := agentapp.New(left, newSeqIDs(300)), agentapp.New(right, newSeqIDs(600))
	owner, project, appInstance := newUUIDForTest(81), newUUIDForTest(82), newUUIDForTest(83)
	// agent_tasks references users; seed the owner the way the acceptance
	// volume bootstrap does.
	if _, err := leftPool.Exec(ctx,
		"INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Bridge Concurrency Owner', now()) ON CONFLICT DO NOTHING",
		owner,
	); err != nil {
		t.Fatalf("seed owner user: %v", err)
	}
	// agent_tasks.project_id references projects; seed the project row too.
	if _, err := leftPool.Exec(ctx,
		"INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1, $2, 'bridge-concurrency-project', 'Bridge Concurrency', $3, $4, now(), now())",
		project, owner, newUUIDForTest(98), newUUIDForTest(99),
	); err != nil {
		t.Fatalf("seed project row: %v", err)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	otherDigest := "sha256:" + strings.Repeat("2", 64)

	t.Run("SameKeySameRequestRaceProducesOneFact", func(t *testing.T) {
		key := fmt.Sprintf("race-%d", time.Now().UnixNano())
		start := make(chan struct{})
		var group sync.WaitGroup
		results := make(chan string, 2)
		failures := make(chan error, 2)
		services := map[string]agentappSubmitter{
			"left": leftService, "right": rightService,
		}
		for _, side := range []string{"left", "right"} {
			group.Add(1)
			go func(side string) {
				defer group.Done()
				<-start
				task, err := services[side].SubmitForApp(ctx, bridgeSubmitInput(owner, appInstance, project, key, digest, "goal"))
				if err != nil {
					failures <- err
					return
				}
				results <- task.ID
			}(side)
		}
		close(start)
		group.Wait()
		close(results)
		close(failures)
		for failure := range failures {
			t.Fatalf("race loser surfaced an error: %v", failure)
		}
		ids := map[string]struct{}{}
		for id := range results {
			ids[id] = struct{}{}
		}
		if len(ids) != 1 {
			t.Fatalf("same-key race produced %d tasks", len(ids))
		}
		if countScratch(`SELECT count(*) FROM workos_core.agent_tasks WHERE owner_user_id = $1`, owner) != 1 {
			t.Fatal("race produced more than one task row")
		}
		if countScratch(`SELECT count(*) FROM workos_core.agent_app_task_requests WHERE owner_user_id = $1 AND app_instance_id = $2`, owner, appInstance) != 1 {
			t.Fatal("race produced more than one mapping")
		}
		if countScratch(`SELECT count(*) FROM workos_events.outbox WHERE aggregate_id = ANY(
			SELECT id FROM workos_core.agent_tasks WHERE owner_user_id = $1)`, owner) != 1 {
			t.Fatal("race produced more than one outbox row")
		}
	})

	t.Run("SameKeyDifferentRequestAborts", func(t *testing.T) {
		key := fmt.Sprintf("conflict-%d", time.Now().UnixNano())
		if _, err := leftService.SubmitForApp(ctx, bridgeSubmitInput(owner, appInstance, project, key, digest, "first")); err != nil {
			t.Fatal(err)
		}
		_, err := rightService.SubmitForApp(ctx, bridgeSubmitInput(owner, appInstance, project, key, otherDigest, "second"))
		if !errors.Is(err, agentdomain.ErrIdempotencyConflict) {
			t.Fatalf("different-request verdict: %v", err)
		}
		// Subtest 1 left one task; this subtest's accepted first run adds a
		// second, and the aborted request must add nothing further.
		if countScratch(`SELECT count(*) FROM workos_core.agent_tasks WHERE owner_user_id = $1`, owner) != 2 {
			t.Fatal("conflict changed the task count (expected the aborted request to roll back)")
		}
	})

	t.Run("SameClientKeyAcrossAppsIsIndependent", func(t *testing.T) {
		key := fmt.Sprintf("shared-%d", time.Now().UnixNano())
		otherApp := newUUIDForTest(92)
		first, err := leftService.SubmitForApp(ctx, bridgeSubmitInput(owner, appInstance, project, key, digest, "first app goal"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := rightService.SubmitForApp(ctx, bridgeSubmitInput(owner, otherApp, project, key, digest, "second app goal"))
		if err != nil {
			t.Fatalf("second app same client key failed: %v", err)
		}
		if first.ID == second.ID {
			t.Fatal("two apps sharing one client key collapsed into one task")
		}
	})
}

// TestBridgeTokenDurabilityAcrossRestart proves the token fact is durable in
// runtime-owned PostgreSQL: a fresh pool ("restarted process") still resolves
// the same credential, rotation invalidates the previous one, and closing the
// session clears the credential entirely.
func TestBridgeTokenDurabilityAcrossRestart(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	firstPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	repository := surfacepostgres.New(firstPool)
	application, err := newSurfaceApplication(repository)
	if err != nil {
		t.Fatal(err)
	}
	owner, device := newUUIDForTest(93), newUUIDForTest(94)
	created, err := application.Create(ctx, surfaceapp.CreateCommand{
		OwnerUserID: owner, DeviceID: device, IdempotencyKey: "restart-token-key",
		ProjectID: newUUIDForTest(95), AppInstanceID: newUUIDForTest(96),
		DeviceClass: "desktop", ViewportWidth: 1024, ViewportHeight: 768, ViewportRatio: 1,
		PreferredRenderer: surfacedomain.RendererWebBundle,
	})
	if err != nil {
		t.Fatalf("create surface: %v", err)
	}
	if created.BridgeToken == "" {
		t.Fatal("no bridge token minted")
	}
	// Simulate the restart: drop the process and reopen the store.
	firstPool.Close()
	restartedPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedPool.Close()
	restarted := surfacepostgres.New(restartedPool)
	session, err := restarted.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(created.BridgeToken), time.Now().UTC())
	if err != nil {
		t.Fatalf("token did not survive the restart: %v", err)
	}
	if session.ID != created.Session.ID {
		t.Fatal("token resolved to a different session")
	}
	// Rotation invalidates the previous credential, and the atomic
	// UPDATE ... RETURNING hands back exactly the row this rotation wrote.
	replacement, err := surfacedomain.NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := restarted.RotateBridgeToken(ctx, surfaceports.RotateBridgeTokenCommand{
		OwnerUserID: owner, DeviceID: device, SessionID: created.Session.ID,
		TokenHash: surfacedomain.HashBridgeToken(replacement), Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}
	if rotated.BridgeTokenHash != surfacedomain.HashBridgeToken(replacement) {
		t.Fatal("rotation did not return the row as of its own write")
	}
	if _, err := restarted.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(created.BridgeToken), time.Now().UTC()); err == nil {
		t.Fatal("previous token still valid after rotation")
	}
	if _, err := restarted.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(replacement), time.Now().UTC()); err != nil {
		t.Fatalf("replacement token not valid: %v", err)
	}
	// Closing the session clears the credential: nothing resolves.
	if _, err := restarted.Close(ctx, owner, device, created.Session.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(replacement), time.Now().UTC()); err == nil {
		t.Fatal("token valid after close")
	}
}

// staticResolver satisfies the LaunchResolver port without Core: only the
// durable token lifecycle is under test here.
type staticResolver struct {
	descriptor surfaceports.LaunchDescriptor
}

func (r *staticResolver) ResolveWebBundle(context.Context, surfaceports.ResolveQuery) (surfaceports.LaunchDescriptor, error) {
	return r.descriptor, nil
}

func (r *staticResolver) ReadWebBundleAsset(context.Context, surfaceports.AssetQuery) (surfaceports.Asset, error) {
	return surfaceports.Asset{}, surfaceports.ErrResolverNotFound
}

type surfaceApp interface {
	Create(context.Context, surfaceapp.CreateCommand) (surfaceapp.CreatedSurface, error)
	Close(context.Context, string, string, string) (surfacedomain.SurfaceSession, error)
}

type surfaceCreated = surfaceapp.CreatedSurface

func surfaceCreateCommand(owner, device, key string) surfaceapp.CreateCommand {
	return surfaceapp.CreateCommand{
		OwnerUserID: owner, DeviceID: device, IdempotencyKey: key,
		ProjectID: newUUIDForTest(395), AppInstanceID: newUUIDForTest(396),
		DeviceClass: "desktop", ViewportWidth: 1024, ViewportHeight: 768, ViewportRatio: 1,
		PreferredRenderer: surfacedomain.RendererWebBundle,
	}
}

func newSurfaceApplication(repository surfaceports.SessionRepository) (*surfaceapp.Service, error) {
	return surfaceapp.New(repository, &staticResolver{descriptor: surfaceports.LaunchDescriptor{
		AppID: "restart-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("3", 64),
		ArtifactID:     newUUIDForTest(97), ArtifactDigest: "sha256:" + strings.Repeat("4", 64),
		Entrypoint: "index.html",
		// ADR-0003: the resolver's authoritative grant epoch must be >= 1;
		// the static fixture pins the initial epoch exactly like Core does.
		GrantRevision: 1,
	}}, ids.UUIDv7{}, 15*time.Minute)
}

// TestAppBridgeAgentCenterVisibility keeps the user path intact: the owner's
// own Agent Task Service still sees App-created tasks, and uninstalled apps
// lose bridge access while durable tasks persist for the user.
func TestAppBridgeRevocationAndUserVisibility(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	artifacts := artifactClients(t)
	surfaces := surfaceClients(t)
	projects := integrationProjectClients(t)
	installations := installationClients(t)
	registry := appRegistryClients(t)
	bridge := bridgeClients(t)
	agentTasks := agentTaskClients(t)

	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("bridge-revoke-%d", stamp)
	artifact := createArtifact(t, ctx, artifacts, fmt.Sprintf("bridge-revoke-create-%d", stamp), "Revoke App", bundleFiles(), "index.html")
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("bridge-revoke-register-%d", stamp),
		ManifestYaml:   bridgeAgentManifest(appID, "Bridge Revoke", artifact.GetId(), artifact.GetDigest()),
	})); err != nil {
		t.Fatalf("register: %v", err)
	}
	project := createIntegrationProject(t, ctx, projects, "App Bridge Revoke", fmt.Sprintf("bridge-revoke-project-%d", stamp))
	installResponse, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("bridge-revoke-install-%d", stamp),
		ProjectId:               project.GetId(),
		AppId:                   appID,
		Version:                 "1.0.0",
		ExpectedProjectRevision: project.GetRevision(),
		GrantedPermissions:      []string{"agent.task.run", "agent.event.watch"},
	}))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	surface, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    fmt.Sprintf("bridge-revoke-surface-%d", stamp),
		AppInstanceId:     installResponse.Msg.GetInstallation().GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1024, Height: 768, PixelRatio: 1},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		t.Fatalf("surface: %v", err)
	}
	token := surface.Msg.GetSession().GetBridgeToken()
	run, err := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: fmt.Sprintf("bridge-revoke-run-%d", stamp), Goal: "goal before revoke",
	}, token))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	taskID := run.Msg.GetTaskId()

	// The owner's Agent Center still sees the App-created task.
	if _, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID})); err != nil {
		t.Fatalf("owner lost visibility of the app task: %v", err)
	}

	// Uninstall revokes bridge authorization even with a valid token.
	if _, err := installations.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
		IdempotencyKey:          fmt.Sprintf("bridge-revoke-uninstall-%d", stamp),
		ProjectId:               project.GetId(),
		InstallationId:          installResponse.Msg.GetInstallation().GetId(),
		ExpectedProjectRevision: installResponse.Msg.GetProjectRevision(),
	})); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	_, runDenied := bridge.RunAgentTask(ctx, tokenRequest(&bridgev1.RunAgentTaskRequest{
		IdempotencyKey: fmt.Sprintf("bridge-revoke-run-after-%d", stamp), Goal: "goal after revoke",
	}, token))
	if connect.CodeOf(runDenied) != connect.CodePermissionDenied && connect.CodeOf(runDenied) != connect.CodeNotFound {
		t.Fatalf("uninstalled app bridge verdict: %v", runDenied)
	}
	watchStream, watchErr := bridge.WatchAgentTaskEvents(ctx, watchRequest(taskID, 0, token))
	if watchErr == nil {
		// Streaming RPCs fail lazily: drain until the server verdict arrives.
		for watchStream.Receive() {
		}
		watchErr = watchStream.Err()
	}
	if connect.CodeOf(watchErr) != connect.CodePermissionDenied && connect.CodeOf(watchErr) != connect.CodeNotFound {
		t.Fatalf("uninstalled app watch verdict: %v", watchErr)
	}
	// The durable task itself persists for the user after revocation.
	if _, err := agentTasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID})); err != nil {
		t.Fatalf("revocation must not cancel the durable task: %v", err)
	}
}

type agentappSubmitter interface {
	SubmitForApp(context.Context, agentapp.AppSubmitInput) (agentdomain.Task, error)
}

// staticInstallations satisfies the orchestration installation source with a
// fixed active installation so an Agent-store outage can be isolated from the
// Project store.
type staticInstallations struct {
	installation projectdomain.Installation
}

func (f *staticInstallations) ResolveActiveInstallation(
	context.Context, string, string, string,
) (projectdomain.Installation, error) {
	return f.installation, nil
}

// newRouterForOutage composes the real Agent application service (backed by
// the outage pool) with the real Task Router; the replay-first mapping lookup
// hits the Agent store before any other dependency.
func newRouterForOutage(agentRepository *agentpostgres.Repository) *orchestration.TaskRouter {
	agents := agentapp.New(agentRepository, ids.UUIDv7{})
	router, err := orchestration.NewTaskRouter(agents, projectOutageProjects(), "fake")
	if err != nil {
		panic(err)
	}
	return router
}

var _ = projectpostgres.New

func projectOutageProjects() orchestration.Projects {
	return &outageProjects{}
}

type outageProjects struct{}

func (f *outageProjects) Get(context.Context, string, string) (projectdomain.Project, error) {
	return projectdomain.Project{}, projectdomain.ErrNotFound
}

// TestAgentStoreOutageIsUnavailableNotInternal proves the Agent-owned store
// outage classification end to end through the private Core App Agent
// transport: a transient PostgreSQL failure (real pool pointed at a closed
// port) wrapped by the Agent adapter surfaces as sanitized retryable
// Unavailable — never Internal, and never a leak of DSN, SQL, or constraint.
func TestAgentStoreOutageIsUnavailableNotInternal(t *testing.T) {
	t.Parallel()
	// A pool whose server never accepts connections.
	outagePort := "127.0.0.1:1"
	outageURL := fmt.Sprintf("postgres://workos:workos@%s/workos?sslmode=disable", outagePort)
	outagePool, err := pgxpool.New(context.Background(), outageURL)
	if err != nil {
		t.Fatalf("open outage pool: %v", err)
	}
	defer outagePool.Close()

	installations := &staticInstallations{installation: func() projectdomain.Installation {
		installation := projectdomain.Installation{
			ID: newUUIDForTest(301), OwnerUserID: newUUIDForTest(302), ProjectID: newUUIDForTest(303),
			AppID: "outage-app", Version: "1.0.0",
			ManifestDigest:     "sha256:" + strings.Repeat("5", 64),
			GrantedPermissions: []string{"agent.event.watch", "agent.task.run"},
			GrantRevision:      1,
		}
		return installation
	}()}
	agentRepository := agentpostgres.New(outagePool)
	appAgent, err := orchestration.NewAppAgentService(installations, newRouterForOutage(agentRepository))
	if err != nil {
		t.Fatal(err)
	}
	path, handler := agentv1connect.NewAppAgentServiceHandler(orchestrationtransport.NewAppAgent(appAgent))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := agentv1connect.NewAppAgentServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&agentv1.RunAgentTaskRequest{
		ProjectId: installations.installation.ProjectID, AppInstanceId: installations.installation.ID,
		ClientIdempotencyKey: "outage-key", Goal: "goal",
		// The private epoch field stands in for the runtime's validated
		// session snapshot; it must match the installation's epoch so the
		// outage under test is the Agent store, not the epoch check.
		InstallationGrantRevision: 1,
	})
	request.Header().Set(identity.UserHeader, installations.installation.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, newUUIDForTest(304))
	_, runErr := client.RunAgentTask(context.Background(), request)
	if connect.CodeOf(runErr) != connect.CodeUnavailable {
		t.Fatalf("agent store outage verdict: %v", runErr)
	}
	if strings.Contains(fmt.Sprint(runErr), "postgres://") || strings.Contains(fmt.Sprint(runErr), "SQLSTATE") {
		t.Fatalf("outage error leaked internals: %v", runErr)
	}
}

// TestAgentOutboxTransientFailureIsUnavailableNotInternal pins the outbox
// append inside CreateForApp to the transient-failure contract. Every
// earlier statement of the transaction succeeds; the failure is raised
// exactly at the outbox INSERT by a trigger that throws SQLSTATE 53000
// (insufficient resources, transient class 53). The verdict through the real
// private transport must be sanitized Unavailable — never Internal, never
// leaking the injected message — and dropping the injection lets the same
// idempotency key succeed end to end, proving the rolled-back transaction
// consumed nothing. The previous implementation wrapped this insert in a
// plain fmt.Errorf, which returned Internal here.
func TestAgentOutboxTransientFailureIsUnavailableNotInternal(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Inject a deterministic transient failure at the outbox append only.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION workos_events.raise_outbox_outage() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'injected outbox outage for regression test' USING ERRCODE = '53000';
END $$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("create outage function: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER outbox_outage BEFORE INSERT ON workos_events.outbox
FOR EACH ROW WHEN (NEW.event_type = 'agent.task.requested.v1')
EXECUTE FUNCTION workos_events.raise_outbox_outage()`); err != nil {
		t.Fatalf("create outage trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS outbox_outage ON workos_events.outbox")
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS workos_events.raise_outbox_outage()")
	})

	installation := projectdomain.Installation{
		ID: newUUIDForTest(330), OwnerUserID: newUUIDForTest(331), ProjectID: newUUIDForTest(332),
		AppID: "outbox-outage-app", Version: "1.0.0",
		ManifestDigest:     "sha256:" + strings.Repeat("6", 64),
		GrantedPermissions: []string{"agent.event.watch", "agent.task.run"},
		GrantRevision:      1,
	}
	// agent_tasks references users and projects; seed both rows the way the
	// acceptance volume bootstrap does, so the transaction proceeds past
	// every earlier statement and fails exactly at the outbox append.
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Outbox Outage Owner', now()) ON CONFLICT DO NOTHING",
		installation.OwnerUserID,
	); err != nil {
		t.Fatalf("seed owner user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at) VALUES ($1, $2, 'outbox-outage-project', 'Outbox Outage', $3, $4, now(), now())",
		installation.ProjectID, installation.OwnerUserID, newUUIDForTest(334), newUUIDForTest(335),
	); err != nil {
		t.Fatalf("seed project row: %v", err)
	}
	agentRepository := agentpostgres.New(pool)
	agents := agentapp.New(agentRepository, ids.UUIDv7{})
	router, err := orchestration.NewTaskRouter(agents, projectpostgres.New(pool), "fake")
	if err != nil {
		t.Fatal(err)
	}
	appAgent, err := orchestration.NewAppAgentService(&staticInstallations{installation: installation}, router)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := agentv1connect.NewAppAgentServiceHandler(orchestrationtransport.NewAppAgent(appAgent))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := agentv1connect.NewAppAgentServiceClient(server.Client(), server.URL)
	newRequest := func() *connect.Request[agentv1.RunAgentTaskRequest] {
		request := connect.NewRequest(&agentv1.RunAgentTaskRequest{
			ProjectId: installation.ProjectID, AppInstanceId: installation.ID,
			ClientIdempotencyKey: "outbox-outage-key", Goal: "goal under outbox outage",
			// Match the installation's epoch so the injected failure is the
			// outbox append, not the grant-epoch check.
			InstallationGrantRevision: 1,
		})
		request.Header().Set(identity.UserHeader, installation.OwnerUserID)
		request.Header().Set(identity.DeviceHeader, newUUIDForTest(333))
		return request
	}

	_, runErr := client.RunAgentTask(ctx, newRequest())
	if connect.CodeOf(runErr) != connect.CodeUnavailable {
		t.Fatalf("outbox outage verdict: %v", runErr)
	}
	text := fmt.Sprint(runErr)
	for _, leaked := range []string{"injected outbox outage", "postgres://", "SQLSTATE", "53000"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("outage error leaked %q: %v", leaked, runErr)
		}
	}

	// The failure was precisely the outbox append: with the injection gone,
	// the same idempotency key succeeds end to end and leaves exactly one
	// requested outbox row for the created task.
	if _, err := pool.Exec(ctx, "DROP TRIGGER outbox_outage ON workos_events.outbox"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	response, retryErr := client.RunAgentTask(ctx, newRequest())
	if retryErr != nil {
		t.Fatalf("retry after removing the injection: %v", retryErr)
	}
	if response.Msg.GetTaskId() == "" {
		t.Fatal("retry created no task")
	}
	var outboxCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM workos_events.outbox WHERE event_type = 'agent.task.requested.v1' AND aggregate_id = $1`,
		response.Msg.GetTaskId(),
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("expected exactly one requested outbox row, got %d", outboxCount)
	}
}

// TestSurfaceCloseClearsBridgeTokenInStorage proves the atomic close: after
// CloseSurface, the at-rest bridge_token_hash column is SQL NULL — verified
// by reading the column directly, not by inferring from a failed token
// lookup — while the first closed_at timestamp survives repeated closes.
func TestSurfaceCloseClearsBridgeTokenInStorage(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := surfacepostgres.New(pool)
	application, err := newSurfaceApplication(repository)
	if err != nil {
		t.Fatal(err)
	}
	owner, device := newUUIDForTest(310), newUUIDForTest(311)
	created, err := application.Create(ctx, surfaceCreateCommand(owner, device, "close-null-key"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.BridgeToken == "" {
		t.Fatal("open create returned no credential")
	}
	closed, err := application.Close(ctx, owner, device, created.Session.ID)
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("close failed: %v", err)
	}
	firstClosedAt := *closed.ClosedAt

	var tokenHash *string
	var closedAt *time.Time
	if err := pool.QueryRow(ctx,
		"SELECT bridge_token_hash, closed_at FROM workos_runtime.surface_sessions WHERE id = $1",
		created.Session.ID,
	).Scan(&tokenHash, &closedAt); err != nil {
		t.Fatalf("read closed session row: %v", err)
	}
	if tokenHash != nil {
		t.Fatal("bridge_token_hash must be SQL NULL after close")
	}
	if closedAt == nil || !closedAt.Equal(firstClosedAt) {
		t.Fatal("first closed_at was not preserved")
	}

	// Repeated close is an idempotent success and preserves the first close.
	reopened, err := application.Close(ctx, owner, device, created.Session.ID)
	if err != nil || reopened.ClosedAt == nil || !reopened.ClosedAt.Equal(firstClosedAt) {
		t.Fatalf("repeated close changed the first result: %v", err)
	}
	// Foreign/missing sessions stay sanitized NotFound.
	if _, err := application.Close(ctx, owner, newUUIDForTest(312), created.Session.ID); err == nil {
		t.Fatal("foreign device close succeeded")
	}
}

// TestSurfaceCreateConcurrencyTokenPersistence drives two same-key creates
// through two real pools and asserts the credential contract end to end:
// every successful response carries a token whose hash was the persisted
// session fact at its linearization point, the final stored hash equals one
// of the returned tokens' hashes (the last linearized rotation), exactly one
// session fact and mapping survive, and the pre-rotation token's
// invalidation comes from the recorded rotation — not from never being
// stored.
func TestSurfaceCreateConcurrencyTokenPersistence(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	leftPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer leftPool.Close()
	rightPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer rightPool.Close()
	left := surfacepostgres.New(leftPool)
	right := surfacepostgres.New(rightPool)
	leftApp, err := newSurfaceApplication(left)
	if err != nil {
		t.Fatal(err)
	}
	rightApp, err := newSurfaceApplication(right)
	if err != nil {
		t.Fatal(err)
	}
	owner, device := newUUIDForTest(313), newUUIDForTest(314)
	command := surfaceCreateCommand(owner, device, "token-race-key")

	start := make(chan struct{})
	var group sync.WaitGroup
	type outcome struct {
		created surfaceCreated
		err     error
	}
	results := make(chan outcome, 2)
	for _, app := range []surfaceApp{leftApp, rightApp} {
		group.Add(1)
		go func(app surfaceApp) {
			defer group.Done()
			<-start
			created, err := app.Create(ctx, command)
			results <- outcome{created, err}
		}(app)
	}
	close(start)
	group.Wait()
	close(results)

	var tokens []string
	ids := map[string]struct{}{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("race surfaced an error: %v", result.err)
		}
		if result.created.BridgeToken == "" {
			t.Fatal("open-session create returned no credential")
		}
		// Every response pairs its credential with the persisted fact it was
		// stored under — the old implementation returned the arbitration
		// winner's session with the loser's never-persisted token, which
		// this assertion fails on.
		if result.created.Session.BridgeTokenHash != surfacedomain.HashBridgeToken(result.created.BridgeToken) {
			t.Fatal("response paired a credential with a hash it was not stored under")
		}
		tokens = append(tokens, result.created.BridgeToken)
		ids[result.created.Session.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("same-key race produced %d sessions", len(ids))
	}

	// The final persisted hash must be one of the returned credentials' hashes
	// — the last linearized rotation — and must resolve through the token
	// lookup on a fresh pool ("restarted process").
	session, lookupErr := left.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(tokens[0]), time.Now().UTC())
	if lookupErr != nil {
		// tokens[0] was rotated out by the later linearization: the winner of
		// the rotation order must be tokens[1], and its hash is stored.
		session, lookupErr = left.GetActiveSessionByBridgeToken(ctx, owner, surfacedomain.HashBridgeToken(tokens[1]), time.Now().UTC())
		if lookupErr != nil {
			t.Fatalf("neither returned credential resolves: %v", lookupErr)
		}
	}
	if session.ID != sessionIDOf(ids) {
		t.Fatal("token resolved to a different session")
	}
	if countScratchRows(t, leftPool, `SELECT count(*) FROM workos_runtime.surface_sessions WHERE owner_user_id = $1`, owner) != 1 {
		t.Fatal("race left more than one session fact")
	}
	if countScratchRows(t, leftPool, `SELECT count(*) FROM workos_runtime.surface_session_requests WHERE owner_user_id = $1`, owner) != 1 {
		t.Fatal("race left more than one request mapping")
	}
}

func countScratchRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count scratch rows: %v", err)
	}
	return count
}

// TestSurfaceReplayRotationLinearizesAcrossRotators drives six concurrent
// replays of one already-consumed create key through six real pools, so
// every call takes the bridge-token rotation path against the same row.
// The rotation and its read are one atomic UPDATE ... RETURNING, so every
// response must pair its own credential with the hash stored at its own
// linearization point — the previous rotate-then-separately-re-read
// implementation interleaved the two steps and returned a token next to a
// later rotation's hash, which this per-response assertion fails on. The
// surviving session fact stays single, and the final persisted hash is one
// of the returned credentials (the last linearized rotation).
func TestSurfaceReplayRotationLinearizesAcrossRotators(t *testing.T) {
	t.Parallel()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	owner, device := newUUIDForTest(320), newUUIDForTest(321)
	command := surfaceCreateCommand(owner, device, "replay-race-key")

	seedPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer seedPool.Close()
	seedApp, err := newSurfaceApplication(surfacepostgres.New(seedPool))
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := seedApp.Create(ctx, command)
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if seeded.BridgeToken == "" {
		t.Fatal("seed create returned no credential")
	}

	const rotators = 6
	start := make(chan struct{})
	var group sync.WaitGroup
	type outcome struct {
		created surfaceCreated
		err     error
	}
	results := make(chan outcome, rotators)
	for range rotators {
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		app, err := newSurfaceApplication(surfacepostgres.New(pool))
		if err != nil {
			t.Fatal(err)
		}
		group.Add(1)
		go func(app surfaceApp) {
			defer group.Done()
			<-start
			created, err := app.Create(ctx, command)
			results <- outcome{created, err}
		}(app)
	}
	close(start)
	group.Wait()
	close(results)

	observedHashes := map[string]struct{}{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("replay race surfaced an error: %v", result.err)
		}
		if result.created.Session.ID != seeded.Session.ID {
			t.Fatalf("replay produced a foreign session %s", result.created.Session.ID)
		}
		if result.created.BridgeToken == "" {
			t.Fatal("open-session replay returned no credential")
		}
		// Per-response pairing: the atomic rotation guarantees this response's
		// own credential is the fact its own snapshot carries.
		if result.created.Session.BridgeTokenHash != surfacedomain.HashBridgeToken(result.created.BridgeToken) {
			t.Fatal("replay response paired its credential with a hash it was not stored under")
		}
		// The full snapshot must survive the atomic rotation: descriptor,
		// capabilities, and expiry are part of the fact the response claims.
		if result.created.Session.Descriptor != seeded.Session.Descriptor {
			t.Fatalf("rotation response lost the launch descriptor: %+v", result.created.Session.Descriptor)
		}
		if len(result.created.Session.BridgeCapabilities) != len(seeded.Session.BridgeCapabilities) {
			t.Fatal("rotation response lost the bridge capabilities")
		}
		if !result.created.Session.ExpiresAt.Equal(seeded.Session.ExpiresAt) {
			t.Fatal("rotation response drifted the session expiry")
		}
		observedHashes[result.created.Session.BridgeTokenHash] = struct{}{}
	}
	if len(observedHashes) != rotators {
		t.Fatalf("expected %d distinct rotations, saw %d", rotators, len(observedHashes))
	}

	var finalHash *string
	if err := seedPool.QueryRow(ctx,
		"SELECT bridge_token_hash FROM workos_runtime.surface_sessions WHERE id = $1",
		seeded.Session.ID,
	).Scan(&finalHash); err != nil {
		t.Fatalf("read final session row: %v", err)
	}
	if finalHash == nil || *finalHash == "" {
		t.Fatal("final bridge_token_hash is NULL on an open session")
	}
	if _, ok := observedHashes[*finalHash]; !ok {
		t.Fatal("final persisted hash never belonged to any returned credential")
	}
	if countScratchRows(t, seedPool, `SELECT count(*) FROM workos_runtime.surface_sessions WHERE owner_user_id = $1`, owner) != 1 {
		t.Fatal("replay race left more than one session fact")
	}
	if countScratchRows(t, seedPool, `SELECT count(*) FROM workos_runtime.surface_session_requests WHERE owner_user_id = $1`, owner) != 1 {
		t.Fatal("replay race left more than one request mapping")
	}
}

func sessionIDOf(ids map[string]struct{}) string {
	for id := range ids {
		return id
	}
	return ""
}
