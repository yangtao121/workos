package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

func TestDelayedLaunchRejectsRetiredGenerationWithoutEngineEffects(t *testing.T) {
	for _, state := range []domain.State{domain.StateStopped, domain.StateStopping, domain.StateFailed, domain.StateStarting, domain.StateRunning} {
		t.Run(string(state), func(t *testing.T) {
			engine, repo := newFakeEngine(), newFakeRepo()
			manager := newTestManager(t, engine, repo)
			old, err := manager.Ensure(context.Background(), testEnsure(testOperation))
			if err != nil {
				t.Fatal(err)
			}
			current := old
			current.State = state
			if state == domain.StateRunning || state == domain.StateStarting {
				current.Generation++
			}
			repo.workloads[old.ID] = current
			old.State = domain.StateStarting
			beforeCreate, beforeStart, beforeRemove := engine.createCalls, engine.startCalls, engine.removeCalls
			if err := manager.driveLaunch(context.Background(), old, domain.WorkloadOperation{}); err == nil {
				t.Fatal("late launch accepted a retired generation")
			}
			if engine.createCalls != beforeCreate || engine.startCalls != beforeStart || engine.removeCalls != beforeRemove {
				t.Fatal("late launch touched an engine object")
			}
		})
	}
}

func TestLaunchRereadUnavailableDoesNotTouchEngine(t *testing.T) {
	engine, repo := newFakeEngine(), newFakeRepo()
	manager := newTestManager(t, engine, repo)
	workload, err := manager.Ensure(context.Background(), testEnsure(testOperation))
	if err != nil {
		t.Fatal(err)
	}
	repo.getErr = ports.ErrStoreUnavailable
	if err := manager.driveLaunch(context.Background(), workload, domain.WorkloadOperation{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("unavailable reread: %v", err)
	}
	if engine.startCalls != 1 || engine.removeCalls != 0 {
		t.Fatal("uncertain durable identity changed the running container")
	}
}

func TestWorkloadLockWaitIsCancellableAndReclaimed(t *testing.T) {
	manager := newTestManager(t, newFakeEngine(), newFakeRepo())
	unlock, err := manager.lockWorkload(context.Background(), "workload")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.lockWorkload(ctx, "workload"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait ignored deadline: %v", err)
	}
	// Unrelated workloads remain independent while the first is locked.
	other, err := manager.lockWorkload(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	unlock()
	if len(manager.operations) != 0 {
		t.Fatal("completed/cancelled operations leaked lock entries")
	}
}

type pausedLaunchEngine struct {
	ports.Engine
	started chan struct{}
	release chan struct{}
}

func (e *pausedLaunchEngine) StartContainer(ctx context.Context, name string) error {
	close(e.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return e.Engine.StartContainer(ctx, name)
	}
}

func TestStopCannotRemoveContainerWhileLaunchIsVerifying(t *testing.T) {
	engine, repo := newFakeEngine(), newFakeRepo()
	manager := newTestManager(t, engine, repo)
	paused := &pausedLaunchEngine{Engine: engine, started: make(chan struct{}), release: make(chan struct{})}
	manager.engine = paused
	result := make(chan error, 1)
	go func() {
		_, err := manager.Ensure(context.Background(), testEnsure(testOperation))
		result <- err
	}()
	<-paused.started
	workload, err := repo.GetActiveByInstance(context.Background(), testOwner, testInstance)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	command := ports.TerminateCommand{WorkloadID: workload.ID, OperationKey: "owner-stop", Reason: "uninstalled"}
	if err := manager.Terminate(ctx, command); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overlapping stop must wait for launch: %v", err)
	}
	close(paused.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := manager.Terminate(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(context.Background(), workload.ID)
	if err != nil || stored.State != domain.StateStopped || engine.containerExists(workload.ContainerName) {
		t.Fatalf("stop did not converge: %+v, %v", stored, err)
	}
}

type createReplyLostEngine struct{ ports.Engine }

func (e createReplyLostEngine) CreateContainer(ctx context.Context, spec ports.ContainerSpec) (string, error) {
	if _, err := e.Engine.CreateContainer(ctx, spec); err != nil {
		return "", err
	}
	return "", ports.ErrContainerAlreadyExists
}

func TestCreateConflictAdoptsVerifiedContainerWithoutDeletingIt(t *testing.T) {
	engine, repo := newFakeEngine(), newFakeRepo()
	manager := newTestManager(t, engine, repo)
	manager.engine = createReplyLostEngine{Engine: engine}
	workload, err := manager.Ensure(context.Background(), testEnsure(testOperation))
	if err != nil || workload.State != domain.StateRunning {
		t.Fatalf("lost-create recovery: %+v %v", workload, err)
	}
	if engine.createCalls != 1 || engine.removeCalls != 0 {
		t.Fatal("create conflict destroyed a valid owned container")
	}
}
