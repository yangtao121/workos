package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/harness/broker"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

const leaseDuration = 30 * time.Second

type Worker struct {
	id           string
	pollInterval time.Duration
	client       taskexecutionv1connect.TaskExecutionServiceClient
	broker       *broker.Broker
	logger       *slog.Logger
}

func New(id, coreURL string, pollInterval time.Duration, value *broker.Broker, logger *slog.Logger) *Worker {
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	return &Worker{
		id: id, pollInterval: pollInterval, broker: value, logger: logger,
		client: taskexecutionv1connect.NewTaskExecutionServiceClient(telemetry.HTTPClient(), coreURL),
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
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	task := lease.GetTask()
	result := make(chan error, 1)
	terminal := make(chan bool, 1)

	go func() {
		sawTerminal := false
		err := w.broker.Run(ctx, task.GetId(), task.GetProviderId(), task.GetInput(), func(event *agentv1.AgentEvent) error {
			if isTerminal(event) {
				sawTerminal = true
			}
			_, appendErr := w.client.AppendTaskEvent(ctx, connect.NewRequest(&taskv1.AppendTaskEventRequest{
				LeaseId: lease.GetLeaseId(), WorkerId: w.id, Event: event,
			}))
			return appendErr
		})
		terminal <- sawTerminal
		result <- err
	}()

	heartbeat := time.NewTicker(leaseDuration / 3)
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
		case err := <-result:
			sawTerminal := <-terminal
			if err != nil && !errors.Is(err, context.Canceled) && !sawTerminal {
				failed := &agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{Reason: err.Error(), Retryable: errors.Is(err, broker.ErrProviderUnavailable)}}}
				if _, appendErr := w.client.AppendTaskEvent(parent, connect.NewRequest(&taskv1.AppendTaskEventRequest{
					LeaseId: lease.GetLeaseId(), WorkerId: w.id, Event: failed,
				})); appendErr != nil {
					w.logger.Warn("append provider failure failed", "task_id", task.GetId(), "error", appendErr)
					return
				}
				sawTerminal = true
			}
			if err == nil && !sawTerminal {
				failed := &agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{Reason: "provider ended without a terminal event"}}}
				if _, appendErr := w.client.AppendTaskEvent(parent, connect.NewRequest(&taskv1.AppendTaskEventRequest{
					LeaseId: lease.GetLeaseId(), WorkerId: w.id, Event: failed,
				})); appendErr != nil {
					w.logger.Warn("append missing terminal failure failed", "task_id", task.GetId(), "error", appendErr)
					return
				}
			}
			if _, finishErr := w.client.FinishTaskLease(parent, connect.NewRequest(&taskv1.FinishTaskLeaseRequest{
				LeaseId: lease.GetLeaseId(), WorkerId: w.id,
			})); finishErr != nil {
				w.logger.Warn("finish task lease failed", "task_id", task.GetId(), "error", finishErr)
			}
			return
		}
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
