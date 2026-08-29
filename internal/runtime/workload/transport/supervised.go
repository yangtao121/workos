// Package transport exposes the private SupervisedWorkloadService. It is
// never registered on the gateway allowlist: only in-process callers and the
// trusted private-network reliability host reach it. Every identity field in
// a response is server-derived; no request can influence runtime IDs,
// endpoints, cgroup paths, or engine names.
package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	workloadv1connect "github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/runtime/workload/application"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

var _ Manager = (*application.Manager)(nil)

// Manager is the application surface the transport needs.
type Manager interface {
	Restart(ctx context.Context, command ports.RestartCommand) (domain.Workload, error)
	Terminate(ctx context.Context, command ports.TerminateCommand) error
	Observe(ctx context.Context) ([]ports.Observation, error)
}

// ApplicationManager adapts the concrete *application.Manager to the
// transport interface.
func ApplicationManager(manager *application.Manager) Manager { return manager }

// NewSupervisedWorkloadHandler wires the private Connect handler.
func NewSupervisedWorkloadHandler(manager Manager) (string, http.Handler) {
	return workloadv1connect.NewSupervisedWorkloadServiceHandler(&supervisedHandler{manager: manager})
}

type supervisedHandler struct {
	manager Manager
}

func (h *supervisedHandler) ListObservations(ctx context.Context, _ *connect.Request[workloadv1.ListObservationsRequest]) (*connect.Response[workloadv1.ListObservationsResponse], error) {
	observations, err := h.manager.Observe(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*workloadv1.WorkloadObservation, 0, len(observations))
	for _, observation := range observations {
		items = append(items, &workloadv1.WorkloadObservation{
			WorkloadId:         observation.WorkloadID,
			Generation:         observation.Generation,
			State:              stateProto(observation.State),
			OwnerUserId:        observation.OwnerUserID,
			ProjectId:          observation.ProjectID,
			AppInstanceId:      observation.AppInstanceID,
			AppId:              observation.AppID,
			ManifestDigest:     observation.ManifestDigest,
			HealthVerdict:      observation.HealthVerdict,
			ExitCategory:       observation.ExitCategory,
			RestartCount:       int32(observation.RestartCount),
			CpuUsageUsec:       observation.CPUUsageUSec,
			MemoryCurrentBytes: observation.MemoryCurrent,
			MemoryPeakBytes:    observation.MemoryPeak,
			MemoryEventsOom:    observation.MemoryOOMs,
			PidsCurrent:        observation.PIDsCurrent,
			PidsEventsPeak:     observation.PIDsPeak,
			Idle:               observation.Idle,
			ObservedAt:         observation.ObservedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return connect.NewResponse(&workloadv1.ListObservationsResponse{Observations: items}), nil
}

func (h *supervisedHandler) RestartWorkload(ctx context.Context, req *connect.Request[workloadv1.RestartWorkloadRequest]) (*connect.Response[workloadv1.RestartWorkloadResponse], error) {
	workload, err := h.manager.Restart(ctx, ports.RestartCommand{
		WorkloadID: req.Msg.GetWorkloadId(), OperationKey: req.Msg.GetActionKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&workloadv1.RestartWorkloadResponse{Generation: workload.Generation}), nil
}

func (h *supervisedHandler) TerminateWorkload(ctx context.Context, req *connect.Request[workloadv1.TerminateWorkloadRequest]) (*connect.Response[workloadv1.TerminateWorkloadResponse], error) {
	err := h.manager.Terminate(ctx, ports.TerminateCommand{
		WorkloadID: req.Msg.GetWorkloadId(), OperationKey: req.Msg.GetActionKey(),
		Reason: req.Msg.GetReason(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&workloadv1.TerminateWorkloadResponse{}), nil
}

// mapError converts manager verdicts to sanitized Connect codes with fixed
// short messages: no endpoints, cgroup paths, container IDs, engine output,
// or image references ever cross the boundary.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("workload request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("workload is not available"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("workload action key was used for a different command"))
	case errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("workload operation is not supported"))
	case errors.Is(err, domain.ErrRunnerUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("verified rootless container capability is unavailable"))
	case errors.Is(err, domain.ErrImageMissing):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("pinned image is not available locally"))
	case errors.Is(err, domain.ErrRestartLimitExhausted):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("workload restart limit is exhausted"))
	case errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("workload manager is temporarily unavailable"))
	case errors.Is(err, domain.ErrCorrupt):
		return connect.NewError(connect.CodeInternal, errors.New("workload stored facts are inconsistent"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("workload operation failed"))
	}
}

func stateProto(state domain.State) workloadv1.SupervisedWorkloadState {
	switch state {
	case domain.StatePending:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_PENDING
	case domain.StateStarting:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STARTING
	case domain.StateRunning:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_RUNNING
	case domain.StateStopping:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STOPPING
	case domain.StateStopped:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STOPPED
	case domain.StateFailed:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_FAILED
	default:
		return workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_UNSPECIFIED
	}
}
