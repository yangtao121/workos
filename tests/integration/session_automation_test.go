//go:build integration

package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

func (d sessionTestDispatcher) DispatchDirective(ctx context.Context, owner, project, provider, key, session string, directive agentdomain.SessionDirective) (agentdomain.Task, error) {
	return d.Dispatch(ctx, owner, project, provider, directive.Objective, key, session)
}

func TestSessionGoalTransactionsAndPauseReplay(t *testing.T) {
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
	owner, project := gen.New(), gen.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Goal fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	tasks, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	repo := agentpostgres.NewSessionRepository(pool)
	service := agentapp.NewSessionService(repo, sessionTestDispatcher{tasks}, gen, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := service.Create(ctx, owner, project, "goal-session", agentapp.SessionSnapshot{ProviderID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	command := agentdomain.SessionDirective{Kind: "create_goal", Objective: "Three fixture rounds", MaxRounds: 3}
	input, _, err := service.SubmitDirective(ctx, owner, session.ID, "create", command)
	if err != nil || input.TaskID == "" || input.Directive == nil || *input.Directive != command {
		t.Fatalf("directive persistence: %+v %v", input, err)
	}
	replay, _, err := service.SubmitDirective(ctx, owner, session.ID, "create", command)
	if err != nil || replay.ID != input.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := command
	changed.MaxRounds = 4
	if _, _, err := service.SubmitDirective(ctx, owner, session.ID, "create", changed); !errors.Is(err, agentdomain.ErrSessionInputConflict) {
		t.Fatalf("changed directive replay: %v", err)
	}
	lease, err := tasks.Claim(ctx, "goal-worker", time.Minute, gen.New(), time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim: %v", err)
	}
	goal := &agentv1.SessionGoal{Ref: "native-goal-ref", Revision: 1, Objective: command.Objective, Phase: "active", MaxRounds: 3, RoundsStarted: 1, Armed: true}
	appendGoal := func() error {
		payload, _ := protojson.Marshal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_GoalUpdated{GoalUpdated: &agentv1.GoalUpdated{Goal: goal}}})
		_, err := tasks.AppendEvent(ctx, lease.ID, "goal-worker", agentdomain.Event{ID: gen.New(), EventType: "goal_updated", Payload: payload, OccurredAt: time.Now().UTC()}, agentdomain.StateRunning, "", "", nil, time.Now().UTC())
		return err
	}
	if err := appendGoal(); err != nil {
		t.Fatal(err)
	}
	saved, err := service.RequestGoalPause(ctx, owner, session.ID, "pause", goal.Ref)
	if err != nil || saved.GoalPauseRef != goal.Ref {
		t.Fatalf("pause intent: %+v %v", saved, err)
	}
	if _, err := service.RequestGoalPause(ctx, gen.New(), session.ID, "pause", goal.Ref); err == nil {
		t.Fatal("foreign pause accepted")
	}
	if _, err := service.RequestGoalPause(ctx, owner, session.ID, "pause", "other-ref"); !errors.Is(err, agentdomain.ErrSessionInputConflict) {
		t.Fatalf("pause replay not bound: %v", err)
	}
	before, err := tasks.Get(ctx, owner, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	goal.Phase = "paused"
	goal.Armed = false
	if err := appendGoal(); !errors.Is(err, agentdomain.ErrInvalid) {
		t.Fatalf("same-revision state mutation: %v", err)
	}
	after, _ := tasks.Get(ctx, owner, input.TaskID)
	if before.LastEventSequence != after.LastEventSequence {
		t.Fatal("invalid projection left committed task event")
	}
	goal.Revision = 2
	if err := appendGoal(); err != nil {
		t.Fatal(err)
	}
	saved, err = service.RequestGoalPause(ctx, owner, session.ID, "pause", goal.Ref)
	if err != nil || saved.Goal.Phase != "paused" || saved.GoalPauseRef != "" {
		t.Fatalf("pause replay after acknowledgement: %+v %v", saved, err)
	}
	goal.Phase = "active"
	goal.Armed = true
	goal.Revision = 3
	if err := appendGoal(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tasks.Cancel(ctx, owner, input.TaskID, "fixture interruption", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	saved, err = service.Get(ctx, owner, session.ID)
	if err != nil || saved.Goal.Armed {
		t.Fatalf("cancel retained automatic authority: %+v %v", saved, err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err = service.Get(ctx, owner, session.ID)
	if err != nil || saved.State != agentdomain.SessionStateNeedsReview {
		t.Fatalf("interruption recovery: %+v %v", saved, err)
	}
}
