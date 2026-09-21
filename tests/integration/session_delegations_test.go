//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	indexfeedpostgres "github.com/yangtao121/workos/internal/core/indexfeed/adapters/postgres"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"google.golang.org/protobuf/encoding/protojson"
)

type delegationDispatcher struct{ sessionTestDispatcher }

func (d delegationDispatcher) Dispatch(ctx context.Context, owner, project, provider, goal, key, session string) (agentdomain.Task, error) {
	payload, _ := json.Marshal(map[string]any{"agentSessionId": session, "goal": goal, "targetScope": map[string]string{"projectId": project}})
	now := time.Now().UTC()
	result, err := d.repository.Create(ctx, agentdomain.Task{ID: (ids.UUIDv7{}).New(), OwnerUserID: owner, ProjectID: project, Input: payload, ProviderID: provider, State: agentdomain.StateQueued, CreatedAt: now, UpdatedAt: now}, key)
	return result.Task, err
}

func TestDelegationGrantsSerializeCapacityAndRetireOnCancellation(t *testing.T) {
	for _, interruption := range []string{"cancellation", "expired-lease"} {
		t.Run(interruption, func(t *testing.T) { testDelegationInterruption(t, interruption) })
	}
}

func testDelegationInterruption(t *testing.T, interruption string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gen := ids.UUIDv7{}
	owner, project, binding, source := gen.New(), gen.New(), gen.New(), "ws_0123456789abcdef"
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Child fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.projects(id,owner_user_id,idempotency_key,name,knowledge_collection_id,artifact_collection_id,created_at,updated_at) VALUES($1,$2,'fixture','Child fixture',$3,$4,now(),now())`, project, owner, gen.New(), gen.New()); err != nil {
		t.Fatal(err)
	}
	tasks, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	service := agentapp.NewSessionService(agentpostgres.NewSessionRepository(pool), delegationDispatcher{sessionTestDispatcher{tasks}}, gen, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := service.Create(ctx, owner, project, "children", agentapp.SessionSnapshot{ProviderID: "fake", WorkspaceBindingID: binding, WorkspaceBindingRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := service.Submit(ctx, owner, session.ID, "run", "Delegate two children")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := tasks.Claim(ctx, "children-worker", time.Minute, gen.New(), time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim: %v", err)
	}
	goalPayload, err := protojson.Marshal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_GoalUpdated{GoalUpdated: &agentv1.GoalUpdated{Goal: &agentv1.SessionGoal{Ref: "interrupted-goal", Revision: 1, Objective: "Run independent children", Phase: "active", MaxRounds: 3, RoundsStarted: 1, Armed: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AppendEvent(ctx, lease.ID, "children-worker", agentdomain.Event{ID: gen.New(), EventType: "goal_updated", Payload: goalPayload, OccurredAt: time.Now().UTC()}, agentdomain.StateRunning, "", "", nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	grants := make([]agentdomain.Delegation, 3)
	failures := make([]error, 3)
	for i := range grants {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grants[i], failures[i] = tasks.AcquireDelegation(ctx, lease.ID, "children-worker", agentdomain.Delegation{Key: gen.New(), Title: "Independent child", BindingID: binding, BindingRevision: 1, SourceID: source}, time.Now().UTC())
		}()
	}
	wg.Wait()
	active := []agentdomain.Delegation{}
	denied := 0
	for i, err := range failures {
		if errors.Is(err, agentdomain.ErrSessionBusy) {
			denied++
		} else if err != nil {
			t.Fatal(err)
		} else {
			active = append(active, grants[i])
		}
	}
	if len(active) != 2 || denied != 1 {
		t.Fatalf("grants=%d denied=%d", len(active), denied)
	}
	child := active[0]
	replay, err := tasks.AcquireDelegation(ctx, lease.ID, "children-worker", child, time.Now().UTC())
	if err != nil || replay.ID != child.ID {
		t.Fatalf("grant replay: %+v %v", replay, err)
	}
	forged := child
	forged.BindingRevision++
	if _, err := tasks.AcquireDelegation(ctx, lease.ID, "children-worker", forged, time.Now().UTC()); !errors.Is(err, agentdomain.ErrProjectDenied) {
		t.Fatalf("stale binding accepted: %v", err)
	}
	if _, err := tasks.GetTaskDelegation(ctx, lease.ID, "different-worker", child.ID, time.Now().UTC()); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("different worker accepted: %v", err)
	}
	child.State, child.WorktreeID, child.BaseCommit = "running", child.ID, strings.Repeat("a", 40)
	child, err = tasks.UpdateTaskDelegation(ctx, lease.ID, "children-worker", child, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	// A result slot belongs to each authorized child. It must not consume the
	// parent's one-per-type slot or allow another child to steal its replay key.
	artifacts := artifactpostgres.New(pool)
	preparer, err := artifactapp.New(artifacts, gen)
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := orchestration.NewTaskArtifactMaterializer(pool, tasks, artifacts, preparer, indexfeedpostgres.New(pool), notificationpostgres.New(pool), gen)
	if err != nil {
		t.Fatal(err)
	}
	patch := []byte("--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new\n")
	second := active[1]
	second.State, second.WorktreeID, second.BaseCommit = "running", second.ID, strings.Repeat("a", 40)
	if _, err := tasks.UpdateTaskDelegation(ctx, lease.ID, "children-worker", second, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var firstArtifact string
	for _, d := range []agentdomain.Delegation{child, second} {
		a, event, err := materializer.MaterializeDelegationArtifact(ctx, lease.ID, "children-worker", d.ID, "Isolated result", patch)
		if err != nil {
			t.Fatal(err)
		}
		replay, replayEvent, err := materializer.MaterializeDelegationArtifact(ctx, lease.ID, "children-worker", d.ID, "Isolated result", patch)
		if err != nil || a.Id != replay.Id || event.Id != replayEvent.Id {
			t.Fatalf("child artifact replay: %v", err)
		}
		if firstArtifact == a.Id {
			t.Fatal("children shared an artifact identity")
		}
		firstArtifact = a.Id
	}
	if _, _, err := materializer.MaterializeDelegationArtifact(ctx, lease.ID, "children-worker", gen.New(), "Forged result", patch); !errors.Is(err, agentdomain.ErrProjectDenied) {
		t.Fatalf("unknown grant published: %v", err)
	}
	if _, _, err := materializer.MaterializeTaskArtifact(ctx, lease.ID, "children-worker", "parent", "Parent result", "code.unified-diff.v1", patch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := materializer.MaterializeTaskArtifact(ctx, lease.ID, "children-worker", "parent-2", "Second parent result", "code.unified-diff.v1", patch); !errors.Is(err, artifactdomain.ErrOutputConflict) {
		t.Fatalf("parent output limit changed: %v", err)
	}
	child.State, child.ResultSummary = "completed", "Independent work completed"
	child, err = tasks.UpdateTaskDelegation(ctx, lease.ID, "children-worker", child, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := tasks.Get(ctx, owner, input.TaskID)
	if _, err := tasks.UpdateTaskDelegation(ctx, lease.ID, "children-worker", child, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after, _ := tasks.Get(ctx, owner, input.TaskID)
	if before.LastEventSequence != after.LastEventSequence {
		t.Fatal("completion replay duplicated event")
	}
	child.ResultSummary = "different outcome"
	if _, err := tasks.UpdateTaskDelegation(ctx, lease.ID, "children-worker", child, time.Now().UTC()); !errors.Is(err, agentdomain.ErrSessionInputConflict) {
		t.Fatalf("changed completion replay: %v", err)
	}
	if interruption == "cancellation" {
		if _, _, err := tasks.Cancel(ctx, owner, input.TaskID, "fixture cancellation", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	} else {
		for range 2 {
			if replay, err := tasks.Claim(ctx, "recovery-worker", time.Minute, gen.New(), lease.ExpiresAt.Add(time.Second)); err != nil || replay != nil {
				t.Fatalf("interrupted execution replayed: %+v %v", replay, err)
			}
		}
	}
	saved, err := service.Get(ctx, owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Delegations) != 2 {
		t.Fatalf("children projection missing: %+v", saved.Delegations)
	}
	if saved.Goal == nil || saved.Goal.Armed {
		t.Fatalf("interrupted goal retained automatic authority: %+v", saved.Goal)
	}
	for _, d := range saved.Delegations {
		if d.ID == active[1].ID && d.State != "needs_review" {
			t.Fatalf("interrupted child remained live: %+v", d)
		}
		if d.ID == child.ID && d.State != "completed" {
			t.Fatalf("settled child outcome overwritten: %+v", d)
		}
	}
	if _, err := tasks.GetTaskDelegation(ctx, lease.ID, "children-worker", active[1].ID, time.Now().UTC()); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("cancelled parent still authorizes child: %v", err)
	}
}
