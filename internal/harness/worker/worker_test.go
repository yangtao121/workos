package worker

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/harness/broker"
	"github.com/yangtao121/workos/internal/harness/ports"
)

// fakeCore implements the TaskExecutionService the worker talks to. Claims
// answer the single lease exactly once, so the polling loop cannot reprocess
// a finished run inside a test window.
type fakeCore struct {
	mu           sync.Mutex
	lease        *taskv1.TaskLease
	claimed      bool
	appended     []*agentv1.AgentEvent
	finished     int
	renewCancels int
}

func (c *fakeCore) ClaimTask(context.Context, *connect.Request[taskv1.ClaimTaskRequest]) (*connect.Response[taskv1.ClaimTaskResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claimed {
		return connect.NewResponse(&taskv1.ClaimTaskResponse{}), nil
	}
	c.claimed = true
	return connect.NewResponse(&taskv1.ClaimTaskResponse{Lease: c.lease}), nil
}

func (c *fakeCore) RenewTaskLease(context.Context, *connect.Request[taskv1.RenewTaskLeaseRequest]) (*connect.Response[taskv1.RenewTaskLeaseResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return connect.NewResponse(&taskv1.RenewTaskLeaseResponse{ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)), CancellationRequested: c.renewCancels > 0}), nil
}

func (c *fakeCore) AppendTaskEvent(_ context.Context, req *connect.Request[taskv1.AppendTaskEventRequest]) (*connect.Response[taskv1.AppendTaskEventResponse], error) {
	c.mu.Lock()
	c.appended = append(c.appended, req.Msg.GetEvent())
	c.mu.Unlock()
	return connect.NewResponse(&taskv1.AppendTaskEventResponse{StoredEvent: req.Msg.GetEvent()}), nil
}

func (c *fakeCore) FinishTaskLease(context.Context, *connect.Request[taskv1.FinishTaskLeaseRequest]) (*connect.Response[taskv1.FinishTaskLeaseResponse], error) {
	c.mu.Lock()
	c.finished++
	c.mu.Unlock()
	return connect.NewResponse(&taskv1.FinishTaskLeaseResponse{}), nil
}

// blockingHarnessProvider never returns until its context is cancelled: it
// stands in for an adapter that ignores deadlines entirely and proves the
// worker cancels the run regardless of adapter cooperation.
type blockingHarnessProvider struct{}

func (blockingHarnessProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "blocking", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (blockingHarnessProvider) Run(ctx context.Context, _ string, _ *agentv1.AgentTaskInput, _ ports.Emit) error {
	<-ctx.Done()
	return ctx.Err()
}

func newWorkerTestServer(t *testing.T, core *fakeCore) *httptest.Server {
	t.Helper()
	_, handler := taskexecutionv1connect.NewTaskExecutionServiceHandler(core)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func TestWorkerEnforcesServerDerivedRuntimeDeadline(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000001", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-1", ProviderId: "blocking",
				Input: &agentv1.AgentTaskInput{
					Goal:   "deadline",
					Budget: &agentv1.AgentBudget{MaxRuntimeSeconds: 1},
				},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)

	worker := New("worker-test", server.URL, 50*time.Millisecond, broker.New(blockingHarnessProvider{}), slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(10 * time.Second)
	for {
		core.mu.Lock()
		var terminal *agentv1.AgentEvent
		for _, event := range core.appended {
			if event.GetRunFailed() != nil || event.GetRunCompleted() != nil || event.GetRunCancelled() != nil {
				terminal = event
			}
		}
		finished := core.finished
		core.mu.Unlock()
		if terminal != nil && finished == 1 {
			if terminal.GetRunFailed() == nil || terminal.GetRunFailed().GetReason() != "provider run exceeded its runtime deadline" {
				t.Fatalf("unexpected terminal event: %#v", terminal)
			}
			if len(core.appended) != 1 {
				t.Fatalf("exactly one terminal event expected, got %d", len(core.appended))
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker never finished the deadline-broken run: terminal=%v finished=%d", terminal, finished)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestWorkerHonoursOwnerCancellationWithoutSyntheticTerminal(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		renewCancels: 1,
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000002", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-2", ProviderId: "blocking",
				Input: &agentv1.AgentTaskInput{Goal: "cancel"},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)

	worker := New("worker-test", server.URL, 20*time.Millisecond, broker.New(blockingHarnessProvider{}), slog.Default())
	worker.heartbeat = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		stopped := len(core.appended) == 0 && core.finished >= 1
		core.mu.Unlock()
		if stopped {
			return
		}
		select {
		case <-deadline:
			t.Fatal("worker never stopped after owner cancellation")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
