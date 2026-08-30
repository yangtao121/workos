package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	credentialv1 "github.com/yangtao121/workos/gen/go/workos/credential/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/harness/broker"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

// CredentialLeases is the worker's view of the Core Credential Vault's
// lease service. It is served only on the Core private mTLS execution
// listener; the client is built by the composition root.
type CredentialLeases interface {
	AcquireTaskCredential(context.Context, *connect.Request[credentialv1.AcquireTaskCredentialRequest]) (*connect.Response[credentialv1.AcquireTaskCredentialResponse], error)
	RenewTaskCredentialLease(context.Context, *connect.Request[credentialv1.RenewTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.RenewTaskCredentialLeaseResponse], error)
	ReleaseTaskCredentialLease(context.Context, *connect.Request[credentialv1.ReleaseTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.ReleaseTaskCredentialLeaseResponse], error)
}

const leaseDuration = 30 * time.Second

// abortGrace is how long a deadline-hit worker keeps waiting for the provider
// to observe the run-context cancellation before abandoning it. A provider
// that ignores cancellation must not pin the lease forever: the worker still
// owns exactly one terminal event and a finished lease (ADR-0005 §5).
const abortGrace = 5 * time.Second

type Worker struct {
	id           string
	pollInterval time.Duration
	// heartbeat is the lease-renewal cadence; it is a field so tests can
	// shorten the 10s default without waiting real lease windows.
	heartbeat time.Duration
	// abandonAfter is the post-deadline grace; a field so tests can shorten
	// the 5s default.
	abandonAfter time.Duration
	client       taskexecutionv1connect.TaskExecutionServiceClient
	credentials  CredentialLeases
	broker       *broker.Broker
	logger       *slog.Logger
}

// New builds the worker. httpClient is the caller-owned transport to the
// Core private execution listener — for the real composition root this is
// the mutually authenticated TLS client; nil falls back to the platform
// default (tests). The worker has no other transport: claim, renew, append,
// finish, artifact, and credential RPCs all ride this one authenticated
// client (ADR-0009).
func New(id, coreURL string, pollInterval time.Duration, value *broker.Broker, logger *slog.Logger, credentials CredentialLeases, httpClient *http.Client) *Worker {
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	if httpClient == nil {
		httpClient = telemetry.HTTPClient()
	}
	return &Worker{
		id: id, pollInterval: pollInterval, heartbeat: leaseDuration / 3, abandonAfter: abortGrace,
		broker: value, logger: logger, credentials: credentials,
		client: taskexecutionv1connect.NewTaskExecutionServiceClient(httpClient, coreURL),
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lease, err := w.claim(ctx)
			if err != nil {
				w.logger.Warn("claim task failed", "error", err)
				continue
			}
			if lease != nil {
				w.process(ctx, lease)
			}
		}
	}
}

func (w *Worker) claim(ctx context.Context) (*taskv1.TaskLease, error) {
	response, err := w.client.ClaimTask(ctx, connect.NewRequest(&taskv1.ClaimTaskRequest{
		WorkerId: w.id, LeaseDuration: durationpb.New(leaseDuration),
	}))
	if err != nil {
		return nil, err
	}
	return response.Msg.GetLease(), nil
}

func (w *Worker) process(parent context.Context, lease *taskv1.TaskLease) {
	runCtx, cancel := context.WithCancel(parent)
	defer cancel()
	task := lease.GetTask()
	// A task whose durable snapshot requires a provider credential derives
	// its short-lived credential lease from the active task lease before any
	// provider starts (ADR-0009). The lease rides the same mTLS execution
	// channel and the same heartbeat: an invalid verdict or an error cancels
	// the run, so the provider child never outlives its authorization.
	var credentialLease *ports.CredentialLease
	if lease.GetRequiresTaskCredential() {
		if w.credentials == nil {
			w.failWithoutProvider(parent, lease, "credential lease service is unavailable")
			return
		}
		response, err := w.credentials.AcquireTaskCredential(parent, connect.NewRequest(&credentialv1.AcquireTaskCredentialRequest{
			TaskLeaseId: lease.GetLeaseId(), WorkerId: w.id,
		}))
		if err != nil {
			w.failWithoutProvider(parent, lease, "provider credential lease is not available")
			return
		}
		if grant := response.Msg; grant.GetRequired() && len(grant.GetSecret()) > 0 {
			credentialLease = &ports.CredentialLease{
				ID: grant.GetCredentialLeaseId(), TaskLeaseID: grant.GetTaskLeaseId(),
				ConsumerID: grant.GetConsumerId(), Purpose: grant.GetPurpose(),
				CredentialRevision: grant.GetCredentialRevision(),
				ExpiresAt:          grant.GetExpiresAt().AsTime(), Secret: grant.GetSecret(),
			}
		} else if grant.GetRequired() {
			w.failWithoutProvider(parent, lease, "provider credential lease is not available")
			return
		}
		defer w.releaseCredentialLease(lease, credentialLease)
	}
	// The server-derived runtime deadline is enforced here, independently of
	// the adapter: even a provider that ignores context cancellation is
	// cancelled with the run, and the fallback below emits exactly one
	// terminal event and finishes the lease (ADR-0005 §5).
	var deadlineHit atomic.Bool
	var deadlineC <-chan time.Time
	if seconds := task.GetInput().GetBudget().GetMaxRuntimeSeconds(); seconds > 0 {
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		deadlineC = timer.C
	}
	// Non-nil once the deadline has fired; the abandon path below then bounds
	// how long a cancellation-ignoring provider may keep the lease.
	var abandonC <-chan time.Time
	// credentialRevoked marks a credential lease that went invalid (revoke,
	// rotate, or lost task lease): unlike an owner cancellation the worker
	// itself owns the deterministic terminal event here.
	var credentialRevoked atomic.Bool
	result := make(chan error, 1)
	terminal := make(chan bool, 1)

	// appendTerminal writes one synthetic terminal event. A failure is logged
	// and never retried here: the caller still attempts FinishTaskLease,
	// which the server only honours once the task is terminal, so a failed
	// append keeps the lease alive exactly when a terminal event is missing.
	appendTerminal := func(event *agentv1.AgentEvent, stage string) {
		if _, appendErr := w.client.AppendTaskEvent(parent, connect.NewRequest(&taskv1.AppendTaskEventRequest{
			LeaseId: lease.GetLeaseId(), WorkerId: w.id, Event: event,
		})); appendErr != nil {
			w.logger.Warn(stage, "task_id", task.GetId(), "error", appendErr)
		}
	}
	finishLease := func(stage string) {
		if _, finishErr := w.client.FinishTaskLease(parent, connect.NewRequest(&taskv1.FinishTaskLeaseRequest{
			LeaseId: lease.GetLeaseId(), WorkerId: w.id,
		})); finishErr != nil {
			w.logger.Warn("finish task lease failed", "task_id", task.GetId(), "stage", stage, "error", finishErr)
		}
	}

	go func() {
		// sawTerminal means a terminal event was durably appended — set only
		// after the append succeeded, never before, so a lost terminal write
		// still gets the deterministic fallback below.
		sawTerminal := false
		// requestedTypes drives the artifact output contract: a provider run
		// may not complete until every requested artifact type was
		// materialized exactly once through the private lease-bound RPC.
		requestedTypes := task.GetInput().GetOutputArtifactTypes()
		emittedTypes := make(map[string]bool, len(requestedTypes))
		artifacts := func(output ports.ArtifactOutput) error {
			if emittedTypes[output.Type] {
				return ports.NewRunError(ports.ErrorKindProtocol, "provider emitted the same artifact type more than once", false, nil)
			}
			response, appendErr := w.client.AppendTaskArtifact(runCtx, connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
				LeaseId: lease.GetLeaseId(), WorkerId: w.id,
				Artifact: artifactOutputProto(output),
			}))
			if appendErr != nil {
				return appendErr
			}
			// The Core-minted artifact reference never flows back to the
			// provider; the durable timeline event is authoritative.
			_ = response
			emittedTypes[output.Type] = true
			return nil
		}
		err := w.broker.Run(runCtx, ports.Execution{
			TaskID: task.GetId(), Input: task.GetInput(), Credential: credentialLease, Artifacts: artifacts,
			Emit: func(event *agentv1.AgentEvent) error {
				// A completion that would leave requested artifact outputs
				// missing fails closed here — before the terminal event lands —
				// so the task deterministically ends with the failure below
				// instead of pretending the artifact contract was fulfilled.
				if _, completed := event.Event.(*agentv1.AgentEvent_RunCompleted); completed && missingArtifactTypes(requestedTypes, emittedTypes) {
					return ports.NewRunError(ports.ErrorKindProtocol, "provider completed without materializing every requested artifact output", false, nil)
				}
				_, appendErr := w.client.AppendTaskEvent(runCtx, connect.NewRequest(&taskv1.AppendTaskEventRequest{
					LeaseId: lease.GetLeaseId(), WorkerId: w.id, Event: event,
				}))
				if appendErr == nil && isTerminal(event) {
					sawTerminal = true
				}
				return appendErr
			}}, task.GetProviderId())
		terminal <- sawTerminal
		result <- err
	}()

	heartbeat := time.NewTicker(w.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-parent.Done():
			return
		case <-heartbeat.C:
			response, err := w.client.RenewTaskLease(parent, connect.NewRequest(&taskv1.RenewTaskLeaseRequest{
				LeaseId: lease.GetLeaseId(), WorkerId: w.id, LeaseDuration: durationpb.New(leaseDuration),
			}))
			if err != nil {
				w.logger.Warn("renew task lease failed", "task_id", task.GetId(), "error", err)
				cancel()
				return
			}
			if response.Msg.GetCancellationRequested() {
				cancel()
			}
			if credentialLease != nil {
				// Revocation, rotation, a lost task lease, or a vault outage
				// must stop the provider child within one bounded heartbeat.
				verdict, renewErr := w.credentials.RenewTaskCredentialLease(parent, connect.NewRequest(&credentialv1.RenewTaskCredentialLeaseRequest{
					CredentialLeaseId: credentialLease.ID, TaskLeaseId: lease.GetLeaseId(), WorkerId: w.id,
				}))
				if renewErr != nil || !verdict.Msg.GetValid() {
					w.logger.Warn("provider credential lease is no longer valid", "task_id", task.GetId())
					// Mirror the deadline path: cancel the run and bound how
					// long a cancellation-ignoring provider may keep the
					// lease; the deterministic terminal below then lands.
					credentialRevoked.Store(true)
					cancel()
					grace := time.NewTimer(w.abandonAfter)
					defer grace.Stop()
					abandonC = grace.C
				}
			}
		case <-deadlineC:
			deadlineHit.Store(true)
			cancel()
			grace := time.NewTimer(w.abandonAfter)
			defer grace.Stop()
			abandonC = grace.C
		case <-abandonC:
			// The provider ignored the cancellation past the grace window.
			// This worker still owns the deterministic terminal event and the
			// lease end; the abandoned run's later appends fail against the
			// finished lease server-side. The reason records which stop path
			// fired: runtime deadline or invalid credential lease.
			if credentialRevoked.Load() {
				w.logger.Warn("provider ignored credential revocation; abandoning", "task_id", task.GetId())
				appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{
					Reason: "provider credential is no longer valid",
				}}}, "append abandoned credential failure failed")
			} else {
				w.logger.Warn("provider ignored run cancellation; abandoning", "task_id", task.GetId())
				appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{
					Reason: "provider run exceeded its runtime deadline",
				}}}, "append abandoned deadline failure failed")
			}
			finishLease("abandoned")
			return
		case err := <-result:
			sawTerminal := <-terminal
			if err != nil && !sawTerminal {
				cancelled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
				switch {
				case cancelled && deadlineHit.Load():
					// The server-derived deadline stopped the run; the task
					// was not cancelled by the owner, so this worker owns the
					// deterministic terminal event.
					appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{
						Reason: "provider run exceeded its runtime deadline",
					}}}, "append deadline failure failed")
				case cancelled && credentialRevoked.Load():
					// The credential lease went invalid: no run_cancelled
					// exists server-side, so this worker owns the terminal.
					appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{
						Reason: "provider credential is no longer valid",
					}}}, "append credential revocation failure failed")
				case cancelled:
					// Owner cancellation: Core has already appended the
					// authoritative run_cancelled terminal event itself.
				default:
					reason, retryable := ports.FailureDetails(err)
					appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{Reason: reason, Retryable: retryable}}}, "append provider failure failed")
				}
			}
			if err == nil && !sawTerminal {
				appendTerminal(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{Reason: "provider ended without a terminal event"}}}, "append missing terminal failure failed")
			}
			// Always attempt the finish: the server-side guard honours it
			// only once the task is terminal, so it cleans up exactly when a
			// terminal event exists and no-ops (keeping the lease) when one
			// is still missing.
			finishLease("result")
			return
		}
	}
}

// failWithoutProvider deterministically terminates a task that could not
// obtain its credential lease before the provider started: exactly one
// non-retryable terminal event and a finished lease. No child process and no
// provider side effect exists at this point.
func (w *Worker) failWithoutProvider(parent context.Context, lease *taskv1.TaskLease, reason string) {
	if _, err := w.client.AppendTaskEvent(parent, connect.NewRequest(&taskv1.AppendTaskEventRequest{
		LeaseId: lease.GetLeaseId(), WorkerId: w.id,
		Event: &agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{Reason: reason}}},
	})); err != nil {
		w.logger.Warn("append credential failure failed", "task_id", lease.GetTask().GetId(), "error", err)
	}
	if _, err := w.client.FinishTaskLease(parent, connect.NewRequest(&taskv1.FinishTaskLeaseRequest{
		LeaseId: lease.GetLeaseId(), WorkerId: w.id,
	})); err != nil {
		w.logger.Warn("finish lease after credential failure failed", "task_id", lease.GetTask().GetId(), "error", err)
	}
}

// releaseCredentialLease is the worker's best-effort defer. Server-side
// expiry bounds the lease regardless: a lost release never extends exposure
// beyond the task lease expiry.
func (w *Worker) releaseCredentialLease(lease *taskv1.TaskLease, credentialLease *ports.CredentialLease) {
	if credentialLease == nil || w.credentials == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.credentials.ReleaseTaskCredentialLease(ctx, connect.NewRequest(&credentialv1.ReleaseTaskCredentialLeaseRequest{
		CredentialLeaseId: credentialLease.ID, TaskLeaseId: lease.GetLeaseId(), WorkerId: w.id,
	})); err != nil {
		w.logger.Warn("release task credential lease failed", "task_id", lease.GetTask().GetId(), "error", err)
	}
}

func isTerminal(event *agentv1.AgentEvent) bool {
	switch event.Event.(type) {
	case *agentv1.AgentEvent_RunCompleted, *agentv1.AgentEvent_RunFailed, *agentv1.AgentEvent_RunCancelled:
		return true
	default:
		return false
	}
}

// missingArtifactTypes reports whether any requested artifact output type has
// not been materialized yet.
func missingArtifactTypes(requested []string, emitted map[string]bool) bool {
	for _, requestedType := range requested {
		if !emitted[requestedType] {
			return true
		}
	}
	return false
}

// artifactOutputProto maps the neutral canonical output to the private wire
// request. Unknown types never reach the sink: the provider contract only
// allows the canonical types Core validates.
func artifactOutputProto(output ports.ArtifactOutput) *taskv1.TaskArtifactOutput {
	result := &taskv1.TaskArtifactOutput{OutputKey: output.Key, Title: output.Title}
	switch output.Type {
	case "document.markdown.v1":
		result.Content = &taskv1.TaskArtifactOutput_Markdown{Markdown: &taskv1.MarkdownArtifactContent{Content: output.Content}}
	case "code.unified-diff.v1":
		result.Content = &taskv1.TaskArtifactOutput_UnifiedDiff{UnifiedDiff: &taskv1.UnifiedDiffArtifactContent{Content: output.Content}}
	}
	return result
}
