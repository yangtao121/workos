package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	credentialv1 "github.com/yangtao121/workos/gen/go/workos/credential/v1"
	"github.com/yangtao121/workos/gen/go/workos/credential/v1/credentialv1connect"
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
	// appendFailures maps an event kind (run_completed, run_failed,
	// run_cancelled) to how many remaining appends of that kind fail before
	// succeeding, modelling transient Core-side append losses.
	appendFailures map[string]int
	// requireTerminalForFinish mirrors the real core: FinishTaskLease only
	// lands once the task is terminal server-side.
	requireTerminalForFinish bool
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
	defer c.mu.Unlock()
	kind := eventKind(req.Msg.GetEvent())
	if remaining := c.appendFailures[kind]; remaining > 0 {
		c.appendFailures[kind] = remaining - 1
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("append temporarily failed"))
	}
	c.appended = append(c.appended, req.Msg.GetEvent())
	return connect.NewResponse(&taskv1.AppendTaskEventResponse{StoredEvent: req.Msg.GetEvent()}), nil
}

func (c *fakeCore) AppendTaskArtifact(_ context.Context, req *connect.Request[taskv1.AppendTaskArtifactRequest]) (*connect.Response[taskv1.AppendTaskArtifactResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remaining := c.appendFailures["artifact"]; remaining > 0 {
		c.appendFailures["artifact"] = remaining - 1
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("artifact append temporarily failed"))
	}
	output := req.Msg.GetArtifact()
	artifactID := "0198d7ea-0000-7000-8000-00000000abcd"
	c.appended = append(c.appended, &agentv1.AgentEvent{
		Id: artifactID, TaskId: req.Msg.GetLeaseId(), Sequence: int64(len(c.appended) + 1),
		Event: &agentv1.AgentEvent_ArtifactCreated{ArtifactCreated: &agentv1.ArtifactCreated{
			ArtifactId: artifactID, ArtifactType: artifactKind(output),
		}},
	})
	return connect.NewResponse(&taskv1.AppendTaskArtifactResponse{
		Artifact: &artifactv1.Artifact{Id: artifactID, Type: artifactKind(output)},
		Event:    c.appended[len(c.appended)-1],
	}), nil
}

// artifactKind names the output's oneof arm for test assertions.
func artifactKind(output *taskv1.TaskArtifactOutput) string {
	switch output.GetContent().(type) {
	case *taskv1.TaskArtifactOutput_Markdown:
		return "document.markdown.v1"
	case *taskv1.TaskArtifactOutput_UnifiedDiff:
		return "code.unified-diff.v1"
	default:
		return "unknown"
	}
}

func (c *fakeCore) ResolveTaskContext(context.Context, *connect.Request[taskv1.ResolveTaskContextRequest]) (*connect.Response[taskv1.ResolveTaskContextResponse], error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no context configured"))
}

func (c *fakeCore) AppendTaskArtifactBatch(context.Context, *connect.Request[taskv1.AppendTaskArtifactBatchRequest]) (*connect.Response[taskv1.AppendTaskArtifactBatchResponse], error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no batch configured"))
}

func (c *fakeCore) FinishTaskLease(context.Context, *connect.Request[taskv1.FinishTaskLeaseRequest]) (*connect.Response[taskv1.FinishTaskLeaseResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.requireTerminalForFinish {
		terminal := false
		for _, event := range c.appended {
			if isTerminal(event) {
				terminal = true
			}
		}
		if !terminal {
			return nil, connect.NewError(connect.CodeAborted, errors.New("lease finish refused: task is not terminal"))
		}
	}
	c.finished++
	return connect.NewResponse(&taskv1.FinishTaskLeaseResponse{}), nil
}

// eventKind names the event's oneof arm for append-failure injection.
func eventKind(event *agentv1.AgentEvent) string {
	switch event.Event.(type) {
	case *agentv1.AgentEvent_RunCompleted:
		return "run_completed"
	case *agentv1.AgentEvent_RunFailed:
		return "run_failed"
	case *agentv1.AgentEvent_RunCancelled:
		return "run_cancelled"
	default:
		return "other"
	}
}

// blockingHarnessProvider never returns until its context is cancelled: it
// stands in for an adapter that ignores deadlines entirely and proves the
// worker cancels the run regardless of adapter cooperation.
type blockingHarnessProvider struct{}

func (blockingHarnessProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "blocking", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (blockingHarnessProvider) Run(ctx context.Context, _ ports.Execution) error {
	<-ctx.Done()
	return ctx.Err()
}

// neverClosed is never written to or closed: receiving from it blocks a
// goroutine forever, context or no context.
var neverClosed = make(chan struct{})

// completingProvider emits one progress event and then a run_completed,
// returning whatever the emit callback returned — the shape of every real
// adapter around its terminal event.
type completingProvider struct{}

func (completingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "completing", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (completingProvider) Run(ctx context.Context, execution ports.Execution) error {
	taskID, input := execution.TaskID, execution.Input
	_ = taskID
	_ = input
	if err := execution.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: "working"}}}); err != nil {
		return err
	}
	return execution.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "done"}}})
}

// stubbornHarnessProvider does not respond to cancellation at all — it never
// returns: the worst kind of adapter the worker must still contain.
type stubbornHarnessProvider struct{}

func (stubbornHarnessProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "stubborn", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (stubbornHarnessProvider) Run(context.Context, ports.Execution) error {
	<-neverClosed
	return nil
}

func newWorkerTestServer(t *testing.T, core *fakeCore) *httptest.Server {
	t.Helper()
	return newWorkerTestServerWithVault(t, core, nil)
}

// newWorkerTestServerWithVault mounts the task execution handler plus an
// optional credential lease handler on one (plain) test server. The lease
// handler's behavior mirrors the core protocol: Acquire answers
// required=false unless a grant is configured; renew returns valid=false
// when configured to simulate revocation.
func newWorkerTestServerWithVault(t *testing.T, core *fakeCore, vault *fakeVault) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	executionPath, execution := taskexecutionv1connect.NewTaskExecutionServiceHandler(core)
	mux.Handle(executionPath, execution)
	if vault != nil {
		leasePath, leaseHandler := credentialv1connect.NewCredentialLeaseServiceHandler(vault)
		mux.Handle(leasePath, leaseHandler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// fakeVault is the worker-test CredentialLeaseService double.
type fakeVault struct {
	mu      sync.Mutex
	acquire int
	grant   *credentialv1.AcquireTaskCredentialResponse
	err     error
	invalid bool
}

func (f *fakeVault) AcquireTaskCredential(_ context.Context, req *connect.Request[credentialv1.AcquireTaskCredentialRequest]) (*connect.Response[credentialv1.AcquireTaskCredentialResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquire++
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(f.grant), nil
}

func (f *fakeVault) RenewTaskCredentialLease(context.Context, *connect.Request[credentialv1.RenewTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.RenewTaskCredentialLeaseResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	valid := !f.invalid
	expires := time.Now().Add(30 * time.Second)
	if !valid {
		expires = time.Time{}
	}
	return connect.NewResponse(&credentialv1.RenewTaskCredentialLeaseResponse{Valid: valid, ExpiresAt: timestamppb.New(expires)}), nil
}

func (f *fakeVault) ReleaseTaskCredentialLease(context.Context, *connect.Request[credentialv1.ReleaseTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.ReleaseTaskCredentialLeaseResponse], error) {
	return connect.NewResponse(&credentialv1.ReleaseTaskCredentialLeaseResponse{}), nil
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

	worker := New("worker-test", server.URL, 50*time.Millisecond, broker.New(blockingHarnessProvider{}), slog.Default(), nil, nil)
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

// TestWorkerAbandonsProviderThatIgnoresCancellation pins the containment
// contract for the worst adapter kind: one that never returns, not even on
// context cancellation. The deadline must still produce exactly one terminal
// event and a finished lease within a bounded window — the worker may not
// renew the lease forever waiting for a provider that never comes back.
// (The abandoned run goroutine stays parked by choice of that provider; the
// worker neither waits for it nor can force it to exit.)
func TestWorkerAbandonsProviderThatIgnoresCancellation(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000003", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-3", ProviderId: "stubborn",
				Input: &agentv1.AgentTaskInput{
					Goal:   "stubborn",
					Budget: &agentv1.AgentBudget{MaxRuntimeSeconds: 1},
				},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)

	worker := New("worker-test", server.URL, 50*time.Millisecond, broker.New(stubbornHarnessProvider{}), slog.Default(), nil, nil)
	worker.abandonAfter = 100 * time.Millisecond
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
			t.Fatalf("worker never abandoned the cancellation-ignoring provider: terminal=%v finished=%d", terminal, finished)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestWorkerRepairsALostTerminalAppend pins the terminal-write ordering: a
// terminal event only counts once Core durably stored it. When the provider's
// run_completed append is lost, the worker must still synthesize exactly one
// run_failed and finish the lease — never skip the fallback because it
// "saw" a terminal that never landed, leaving the task un-terminal and the
// lease unfinishable.
func TestWorkerRepairsALostTerminalAppend(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		requireTerminalForFinish: true,
		appendFailures:           map[string]int{"run_completed": 1},
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000004", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-4", ProviderId: "completing",
				Input: &agentv1.AgentTaskInput{Goal: "lost terminal"},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)

	worker := New("worker-test", server.URL, 20*time.Millisecond, broker.New(completingProvider{}), slog.Default(), nil, nil)
	worker.heartbeat = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		var failed *agentv1.AgentEvent
		for _, event := range core.appended {
			if event.GetRunFailed() != nil {
				failed = event
			}
		}
		finished := core.finished
		core.mu.Unlock()
		if failed != nil && finished == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker never repaired the lost terminal append: failed=%v finished=%d", failed, finished)
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

	worker := New("worker-test", server.URL, 20*time.Millisecond, broker.New(blockingHarnessProvider{}), slog.Default(), nil, nil)
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

// artifactSkippingProvider completes without materializing the artifact
// outputs its task requested — the run must fail closed at the terminal.
type artifactSkippingProvider struct{}

func (artifactSkippingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "skipping", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (artifactSkippingProvider) Run(ctx context.Context, execution ports.Execution) error {
	return execution.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "done"}}})
}

// artifactEmittingProvider materializes the requested output, then completes.
type artifactEmittingProvider struct{}

func (artifactEmittingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "emitting", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (artifactEmittingProvider) Run(ctx context.Context, execution ports.Execution) error {
	if err := execution.Artifacts(ports.ArtifactOutput{Key: "document", Title: "Title", Type: "document.markdown.v1", Content: []byte("# hi\n")}); err != nil {
		return err
	}
	return execution.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "done"}}})
}

func TestWorkerMaterializesRequestedArtifactsBeforeTerminal(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000005", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-5", ProviderId: "emitting",
				Input: &agentv1.AgentTaskInput{Goal: "g", OutputArtifactTypes: []string{"document.markdown.v1"}},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)
	worker := New("worker-test", server.URL, 20*time.Millisecond, broker.New(artifactEmittingProvider{}), slog.Default(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		completed, artifact := false, false
		finished := core.finished
		for _, event := range core.appended {
			if event.GetRunCompleted() != nil {
				completed = true
			}
			if event.GetArtifactCreated() != nil {
				artifact = true
			}
		}
		core.mu.Unlock()
		if completed && artifact && finished == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker never completed the artifact run: completed=%v artifact=%v finished=%d", completed, artifact, finished)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestWorkerFailsRunsThatSkipRequestedArtifacts(t *testing.T) {
	t.Parallel()
	core := &fakeCore{
		lease: &taskv1.TaskLease{
			LeaseId: "018f0000-0000-7000-8000-000000000006", WorkerId: "worker-test",
			Task: &agentv1.AgentTask{
				Id: "task-6", ProviderId: "skipping",
				Input: &agentv1.AgentTaskInput{Goal: "g", OutputArtifactTypes: []string{"document.markdown.v1"}},
			},
			ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
		},
	}
	server := newWorkerTestServer(t, core)
	worker := New("worker-test", server.URL, 20*time.Millisecond, broker.New(artifactSkippingProvider{}), slog.Default(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		var failed *agentv1.AgentEvent
		finished := core.finished
		for _, event := range core.appended {
			if event.GetRunFailed() != nil {
				failed = event
			}
			if event.GetRunCompleted() != nil {
				failed = nil
			}
		}
		core.mu.Unlock()
		if failed != nil && finished == 1 {
			if failed.GetRunFailed().GetReason() != "provider completed without materializing every requested artifact output" {
				t.Fatalf("unexpected failure reason: %q", failed.GetRunFailed().GetReason())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker never failed the artifact-skipping run: failed=%v finished=%d", failed, finished)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// leaseVerifyingProvider refuses to run unless the execution carries a live
// credential lease, proving the worker derives it before the provider starts.
type leaseVerifyingProvider struct{ started chan struct{} }

func (p leaseVerifyingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "lease", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (p leaseVerifyingProvider) Run(ctx context.Context, execution ports.Execution) error {
	if execution.Credential == nil || execution.Credential.ConsumerID != "deepseek" || len(execution.Credential.Secret) == 0 {
		return ports.NewRunError(ports.ErrorKindConfiguration, "lease missing", false, nil)
	}
	close(p.started)
	return execution.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "done"}}})
}

func leaseTask() *taskv1.TaskLease {
	return &taskv1.TaskLease{
		LeaseId: "018f0000-0000-7000-8000-000000000010", WorkerId: "worker-test",
		RequiresTaskCredential: true,
		Task: &agentv1.AgentTask{
			Id: "task-lease", ProviderId: "lease",
			Input: &agentv1.AgentTaskInput{Goal: "lease"},
		},
		ExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second)),
	}
}

func TestWorkerFailsTaskWithoutProviderStartWhenAcquireFails(t *testing.T) {
	t.Parallel()
	core := &fakeCore{lease: leaseTask(), requireTerminalForFinish: true}
	vault := &fakeVault{err: errors.New("vault unavailable")}
	server := newWorkerTestServerWithVault(t, core, vault)
	worker := New("worker-test", server.URL, time.Millisecond, broker.New(leaseVerifyingProvider{started: make(chan struct{})}), slog.Default(), vault, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		failed, finished := false, core.finished
		for _, event := range core.appended {
			if event.GetRunFailed() != nil {
				failed = true
			}
		}
		core.mu.Unlock()
		if failed && finished == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("task did not fail closed without a lease: failed=%v finished=%d", failed, finished)
		default:
		}
	}
}

func TestWorkerAcquiresLeaseBeforeProviderRuns(t *testing.T) {
	t.Parallel()
	core := &fakeCore{lease: leaseTask(), requireTerminalForFinish: true}
	vault := &fakeVault{grant: &credentialv1.AcquireTaskCredentialResponse{
		CredentialLeaseId: "lease-1", Required: true, ConsumerId: "deepseek",
		Purpose: ports.PurposeProviderAPIKeyV1, Secret: []byte("synthetic"),
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	}}
	server := newWorkerTestServerWithVault(t, core, vault)
	started := make(chan struct{})
	worker := New("worker-test", server.URL, time.Millisecond, broker.New(leaseVerifyingProvider{started: started}), slog.Default(), vault, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-started:
			return
		case <-deadline:
			t.Fatal("provider never started on a valid lease")
		default:
		}
	}
}

// leaseBlockingProvider blocks until its run context is cancelled, under the
// provider id the lease task references.
type leaseBlockingProvider struct{}

func (leaseBlockingProvider) Describe() *harnessv1.HarnessProviderInfo {
	return &harnessv1.HarnessProviderInfo{Id: "lease", Health: commonv1.HealthState_HEALTH_STATE_HEALTHY}
}

func (leaseBlockingProvider) Run(ctx context.Context, _ ports.Execution) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestWorkerRevokedCredentialProducesTerminalFailure(t *testing.T) {
	t.Parallel()
	core := &fakeCore{lease: leaseTask(), requireTerminalForFinish: true}
	vault := &fakeVault{grant: &credentialv1.AcquireTaskCredentialResponse{
		CredentialLeaseId: "lease-1", Required: true, ConsumerId: "deepseek",
		Purpose: ports.PurposeProviderAPIKeyV1, Secret: []byte("synthetic"),
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
	}}
	server := newWorkerTestServerWithVault(t, core, vault)
	worker := New("worker-test", server.URL, 5*time.Millisecond, broker.New(leaseBlockingProvider{}), slog.Default(), vault, nil)
	worker.heartbeat = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)
	// Let the run start under a valid lease, then make renewals invalid.
	time.Sleep(50 * time.Millisecond)
	vault.mu.Lock()
	vault.invalid = true
	vault.mu.Unlock()

	deadline := time.After(5 * time.Second)
	for {
		core.mu.Lock()
		revoked, finished := false, core.finished
		var reasons []string
		for _, event := range core.appended {
			if failed := event.GetRunFailed(); failed != nil {
				reasons = append(reasons, failed.GetReason())
				if failed.GetReason() == "provider credential is no longer valid" {
					revoked = true
				}
			}
		}
		core.mu.Unlock()
		if revoked && finished == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("revocation did not produce the deterministic terminal: revoked=%v finished=%d reasons=%q", revoked, finished, reasons)
		default:
		}
	}
}
