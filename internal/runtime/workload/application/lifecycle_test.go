package application

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
	"testing"
	"time"
)

func TestManualStopInstalledAppSurvivesIdleButNotRevocation(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	command := testEnsure("manual")
	command.LifecycleMode = 2
	workload, err := manager.Ensure(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	manager.surfaces.(*fakeSurfaces).has = false
	future := time.Now().UTC().Add(2 * time.Hour)
	manager.now = func() time.Time { return future }
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(ctx, workload.ID)
	if err != nil || row.State != domain.StateRunning || row.LifecycleMode != 2 {
		t.Fatalf("manual app reclaimed as idle: %#v %v", row, err)
	}
	if engine.createCalls != 1 {
		t.Fatal("reconcile duplicated program")
	}
	command.LifecycleMode = 1
	if _, err := manager.Ensure(ctx, command); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("mode drift accepted: %v", err)
	}
	manager.verifier.(*fakeVerifier).verdict = ports.LaunchGone
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = repo.Get(ctx, workload.ID)
	if err != nil || row.State != domain.StateStopped {
		t.Fatalf("revocation ignored: %#v %v", row, err)
	}
}
func TestInstalledManualStopExplicitRestartAndDrift(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("bounded"))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Terminate(ctx, ports.TerminateCommand{WorkloadID: workload.ID, OperationKey: "stop", Reason: "policy"}); err != nil {
		t.Fatal(err)
	}
	command := ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "manual-restart", LifecycleMode: 2}
	restarted, err := manager.Restart(ctx, command)
	if err != nil || restarted.Generation != 2 || restarted.LifecycleMode != 2 {
		t.Fatalf("restart policy: %#v %v", restarted, err)
	}
	replay, err := manager.Restart(ctx, command)
	if err != nil || replay.Generation != 2 || engine.createCalls != 2 {
		t.Fatalf("replay duplicated: %#v %v", replay, err)
	}
	command.LifecycleMode = 1
	if _, err := manager.Restart(ctx, command); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("restart drift accepted: %v", err)
	}
	// A reliability restart omits mode and preserves actual policy.
	automatic, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "automatic"})
	if err != nil || automatic.LifecycleMode != 2 {
		t.Fatalf("automatic restart dropped manual retention: %#v %v", automatic, err)
	}
}
