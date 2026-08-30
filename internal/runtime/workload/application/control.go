package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// Restart deterministically recreates the workload's container from the exact
// pinned image, argv, and effective policy under a new generation. The action
// key makes it idempotent: same key replays the same verdict across runtime
// and caller restarts. The persisted effective restart limit is a hard
// refusal once spent — the crash-loop bound is enforced here, by
// deterministic code, regardless of who asks (ADR-0006 §6).
func (m *Manager) Restart(ctx context.Context, command ports.RestartCommand) (domain.Workload, error) {
	if !domain.ValidWorkloadID(command.WorkloadID) || !domain.ValidOperationKey(command.OperationKey) {
		return domain.Workload{}, domain.ErrInvalid
	}
	workload, err := m.repository.Get(ctx, command.WorkloadID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Workload{}, domain.ErrNotFound
		}
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.Workload{}, domain.ErrUnavailable
		}
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	digest := domain.OperationDigest(domain.OperationRestart, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	stored, err := m.repository.LookupOperation(ctx, workload.ID, command.OperationKey)
	if err != nil {
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	if stored.OperationKey != "" {
		if stored.RequestDigest != digest {
			return domain.Workload{}, domain.ErrIdempotencyConflict
		}
		if stored.Completed() && !stored.Retryable() {
			// Replay the recorded verdict verbatim: a limit-exhausted or
			// otherwise refused decision stays refused on replay, and a
			// completed success replays success. Returning success for a
			// recorded refusal would let the supervisor believe a restart
			// happened twice.
			if err := replayWorkloadError(stored.ErrorKind); err != nil {
				return domain.Workload{}, err
			}
			return workload, nil
		}
	} else {
		now := m.now()
		if err := m.repository.RecordOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
		}
	}
	if workload.State != domain.StateRunning && workload.State != domain.StateFailed {
		return domain.Workload{}, domain.ErrUnsupported
	}
	if workload.RestartCount >= workload.Effective.RestartLimit {
		err := domain.ErrRestartLimitExhausted
		_ = m.repository.RecordOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ErrorKind: domain.ErrorLimitExhausted, ResultGeneration: workload.Generation,
			CreatedAt: m.now(), UpdatedAt: m.now(),
		})
		return domain.Workload{}, err
	}
	next := workload
	next.Generation = workload.Generation + 1
	next.RestartCount = workload.RestartCount + 1
	next.State = domain.StateStarting
	now := m.now()
	if err := m.repository.Transition(ctx, workload.ID, workload.State, domain.StateStarting, ports.WorkloadFacts{
		Generation: next.Generation, RestartCount: next.RestartCount,
		HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone,
		ClearEngine: true,
	}, now); err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.Workload{}, domain.ErrUnavailable
		}
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationRestart, RequestDigest: digest,
	}
	// The old container object must yield its deterministic name before the
	// replacement is created; removal is by the exact persisted identity.
	m.removeContainer(ctx, workload.ContainerID, workload.ContainerName)
	if err := m.driveLaunch(ctx, next, operation); err != nil {
		return domain.Workload{}, err
	}
	result, err := m.repository.Get(ctx, workload.ID)
	if err != nil {
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	if result.State != domain.StateRunning {
		return domain.Workload{}, domain.ErrCorrupt
	}
	return result, nil
}

// Terminate deterministically stops and removes the workload's container by
// its exact identity and parks the row in stopped. Idempotent by action key.
func (m *Manager) Terminate(ctx context.Context, command ports.TerminateCommand) error {
	if !domain.ValidWorkloadID(command.WorkloadID) || !domain.ValidOperationKey(command.OperationKey) ||
		!domain.ValidTerminateReason(command.Reason) {
		return domain.ErrInvalid
	}
	workload, err := m.repository.Get(ctx, command.WorkloadID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrNotFound
		}
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("terminate workload: %w", err)
	}
	digest := m.terminateDigest(workload, command.Reason)
	stored, err := m.repository.LookupOperation(ctx, workload.ID, command.OperationKey)
	if err != nil {
		return fmt.Errorf("terminate workload: %w", err)
	}
	if stored.OperationKey != "" {
		if stored.RequestDigest != digest {
			return domain.ErrIdempotencyConflict
		}
		if stored.Completed() && !stored.Retryable() {
			return nil
		}
	} else {
		now := m.now()
		if err := m.repository.RecordOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationTerminate, RequestDigest: digest,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("terminate workload: %w", err)
		}
	}
	if workload.State.Terminal() {
		return nil
	}
	now := m.now()
	if err := m.repository.Transition(ctx, workload.ID, workload.State, domain.StateStopping, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
	}, now); err != nil && !errors.Is(err, domain.ErrNotFound) {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("terminate workload: %w", err)
	}
	m.removeContainer(ctx, workload.ContainerID, workload.ContainerName)
	stoppedAt := m.now()
	if err := m.repository.Transition(ctx, workload.ID, domain.StateStopping, domain.StateStopped, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
		StoppedAt: &stoppedAt, ClearEngine: true,
	}, stoppedAt); err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("terminate workload: %w", err)
	}
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationTerminate, RequestDigest: digest,
		ResultState: domain.StateStopped, ResultGeneration: workload.Generation,
		CreatedAt: stoppedAt, UpdatedAt: stoppedAt,
	}
	if err := m.repository.RecordOperation(ctx, operation); err != nil {
		return fmt.Errorf("terminate workload: %w", err)
	}
	return nil
}

func (m *Manager) terminateDigest(workload domain.Workload, reason string) string {
	base := domain.OperationDigest(domain.OperationTerminate, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	sum := sha256.Sum256([]byte(base + "|" + reason))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// removeContainer removes by exact ID and exact deterministic name — never by
// prefix, never by wildcard — and treats absence as converged.
func (m *Manager) removeContainer(ctx context.Context, containerID, containerName string) {
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	targets := make([]string, 0, 2)
	if containerID != "" {
		targets = append(targets, containerID)
	}
	if containerName != "" {
		targets = append(targets, containerName)
	}
	for _, target := range targets {
		if err := m.engine.StopContainer(cleanupCtx, target, 10*time.Second); err != nil &&
			!errors.Is(err, ports.ErrContainerNotFound) && !errors.Is(err, ports.ErrEngineUnavailable) {
			m.logger.Warn("workload container stop failed", "error", err)
		}
		if err := m.engine.RemoveContainer(cleanupCtx, target); err != nil &&
			!errors.Is(err, ports.ErrContainerNotFound) && !errors.Is(err, ports.ErrEngineUnavailable) {
			m.logger.Warn("workload container remove failed", "error", err)
		}
	}
}

// replayWorkloadError reconstructs the sanitized error of a recorded
// terminal operation verdict so idempotent replays return exactly the first
// decision, never a fabricated success.
func replayWorkloadError(kind domain.ErrorKind) error {
	switch kind {
	case domain.ErrorLimitExhausted:
		return domain.ErrRestartLimitExhausted
	case domain.ErrorConflict:
		return domain.ErrIdempotencyConflict
	case domain.ErrorUnsupported:
		return domain.ErrUnsupported
	case domain.ErrorInvalid:
		return domain.ErrInvalid
	default:
		return nil
	}
}
