//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

type sessionTestDispatcher struct{ repository *agentpostgres.Repository }

func (d sessionTestDispatcher) Dispatch(ctx context.Context, owner, project, provider, goal, key, session string) (agentdomain.Task, error) {
	payload, _ := json.Marshal(map[string]string{"agentSessionId": session, "goal": goal})
	now := time.Now().UTC()
	result, err := d.repository.Create(ctx, agentdomain.Task{ID: (ids.UUIDv7{}).New(), OwnerUserID: owner, Input: payload, ProviderID: provider, State: agentdomain.StateQueued, CreatedAt: now, UpdatedAt: now}, key)
	return result.Task, err
}
func (d sessionTestDispatcher) Get(ctx context.Context, owner, id string) (agentdomain.Task, error) {
	return d.repository.Get(ctx, owner, id)
}
func (d sessionTestDispatcher) Cancel(ctx context.Context, owner, task, reason string) (agentdomain.Task, error) {
	value, _, err := d.repository.Cancel(ctx, owner, task, reason, time.Now().UTC())
	return value, err
}

func TestSessionTransactionsAndInterruptedLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Session fixture',now())`, owner); err != nil {
		t.Fatal(err)
	}
	tasks, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	repo := agentpostgres.NewSessionRepository(pool)
	service := agentapp.NewSessionService(repo, sessionTestDispatcher{tasks}, gen, slog.New(slog.NewTextHandler(io.Discard, nil)))
	session, err := service.Create(ctx, owner, project, "session", agentapp.SessionSnapshot{ProviderID: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if session.EventSequence != 1 {
		t.Fatal("create lost initial event")
	}
	if _, _, err := service.Submit(ctx, owner, session.ID, "invalid", ""); err == nil {
		t.Fatal("empty input accepted")
	}
	const count = 24
	var wg sync.WaitGroup
	failures := make(chan error, count)
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := service.Submit(ctx, owner, session.ID, fmt.Sprintf("key-%d", i/2), "same input")
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := service.ListInputs(ctx, owner, session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != count/2 {
		t.Fatalf("deduplication: %d", len(inputs))
	}
	dispatched := 0
	for i, input := range inputs {
		if input.Sequence != int64(i+1) {
			t.Fatal("sequence gap")
		}
		if input.State == agentdomain.SessionInputDispatched {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Fatalf("active executions=%d", dispatched)
	}
	if _, _, err := service.Submit(ctx, owner, session.ID, inputs[0].ClientInputID, "different"); !errors.Is(err, agentdomain.ErrSessionInputConflict) {
		t.Fatalf("conflict=%v", err)
	}
	events, err := service.Events(ctx, owner, session.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range events {
		if event.Sequence != int64(i+1) {
			t.Fatal("event cursor gap")
		}
	}
	// Roll back a real SQL mutation; event counter and row remain aligned.
	before, _ := service.Get(ctx, owner, session.ID)
	rollback := errors.New("rollback")
	err = repo.WithinSession(ctx, owner, session.ID, func(locked agentports.SessionRepository) error {
		if _, err := locked.BumpInputSequence(ctx, owner, session.ID, before.InputSequence+1, before.InputSequence, time.Now().UTC()); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	after, _ := service.Get(ctx, owner, session.ID)
	if after.InputSequence != before.InputSequence {
		t.Fatal("failed transaction advanced sequence")
	}

	// Lost execution lease settles once and can never be re-claimed.
	now := time.Now().UTC()
	lease, err := tasks.Claim(ctx, "session-worker", time.Second, gen.New(), now)
	if err != nil || lease == nil {
		t.Fatalf("claim=%v %v", lease, err)
	}
	replay, err := tasks.Claim(ctx, "recovery-worker", time.Second, gen.New(), now.Add(2*time.Second))
	if err != nil || replay != nil {
		t.Fatalf("expired execution replayed=%v %v", replay, err)
	}
	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Get(ctx, owner, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != agentdomain.SessionStateNeedsReview || recovered.ActiveTaskID != "" {
		t.Fatalf("recovery=%+v", recovered)
	}
	if _, _, err := service.Submit(ctx, owner, session.ID, "after-crash", "continue"); err == nil {
		t.Fatal("unknown effects allowed automatic continuation")
	}
	if lease, err := tasks.Claim(ctx, "retry-worker", time.Second, gen.New(), now.Add(3*time.Second)); err != nil || lease != nil {
		t.Fatalf("queued task ran while review pending: %v %v", lease, err)
	}
	if _, err := service.Close(ctx, owner, session.ID); err != nil {
		t.Fatal(err)
	}
	t.Run("finalize beyond first page and replay after service restart", func(t *testing.T) {
		history, err := service.Create(ctx, owner, project, "long-history", agentapp.SessionSnapshot{ProviderID: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		var last agentdomain.SessionInput
		for i := 0; i < 205; i++ {
			last, _, err = service.Submit(ctx, owner, history.ID, fmt.Sprintf("turn-%d", i), "history fixture")
			if err != nil || last.TaskID == "" {
				t.Fatalf("turn %d: %v", i, err)
			}
			if _, err := pool.Exec(ctx, `UPDATE workos_core.agent_tasks SET state='completed' WHERE id=$1`, last.TaskID); err != nil {
				t.Fatal(err)
			}
			if i < 204 {
				if err := service.FinishTaskRun(ctx, owner, history.ID, last.TaskID, agentdomain.SessionInputCompleted, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
		restarted := agentapp.NewSessionService(agentpostgres.NewSessionRepository(pool), sessionTestDispatcher{tasks}, gen, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := restarted.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if err := restarted.FinishTaskRun(ctx, owner, history.ID, last.TaskID, agentdomain.SessionInputCompleted, ""); err != nil {
			t.Fatal(err)
		}
		saved, err := restarted.GetInput(ctx, owner, history.ID, last.ClientInputID)
		if err != nil || saved.State != agentdomain.SessionInputCompleted {
			t.Fatalf("last input=%+v err=%v", saved, err)
		}
		cursor := int64(0)
		for {
			page, err := restarted.Events(ctx, owner, history.ID, cursor, 200)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			for _, event := range page {
				if event.Sequence != cursor+1 {
					t.Fatal("cursor gap after restart")
				}
				cursor = event.Sequence
			}
		}
		if cursor != 1+205*3 {
			t.Fatalf("duplicate or missing terminal event: %d", cursor)
		}
	})

	t.Run("closed session reaps admission committed before input linkage", func(t *testing.T) {
		abandoned, err := service.Create(ctx, owner, project, "abandoned", agentapp.SessionSnapshot{ProviderID: "fake"})
		if err != nil {
			t.Fatal(err)
		}
		inputID := gen.New()
		if _, err := pool.Exec(ctx, `INSERT INTO workos_core.agent_session_inputs(input_id,session_id,owner_user_id,client_input_id,input_text,request_digest,state,sequence,created_at,updated_at) VALUES($1,$2,$3,'crash-window','fixture',$4,'accepted',1,now(),now())`, inputID, abandoned.ID, owner, "sha256:"+strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		task, err := (sessionTestDispatcher{tasks}).Dispatch(ctx, owner, project, "fake", "fixture", fmt.Sprintf("session-%s-input-%s", abandoned.ID, inputID), abandoned.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Close(ctx, owner, abandoned.ID); err != nil {
			t.Fatal(err)
		}
		if err := service.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		saved, err := tasks.Get(ctx, owner, task.ID)
		if err != nil || saved.State != agentdomain.StateCancelled {
			t.Fatalf("orphan admission: %s %v", saved.State, err)
		}
	})

}
