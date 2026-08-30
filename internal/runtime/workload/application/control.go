package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
	targetGeneration := workload.Generation + 1
	stored, err := m.repository.LookupOperation(ctx, workload.ID, command.OperationKey)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.Workload{}, domain.ErrUnavailable
		}
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	if stored.OperationKey != "" {
		if stored.ResultGeneration > 0 {
			targetGeneration = stored.ResultGeneration
		}
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
			result := workload
			if stored.ResultGeneration > 0 {
				result.Generation = stored.ResultGeneration
			}
			return result, nil
		}
		if stored.ResultGeneration > 0 {
			switch {
			case workload.Generation == stored.ResultGeneration && workload.State == domain.StateRunning:
				return m.finalizeRestartReplay(ctx, workload, stored)
			case workload.Generation == stored.ResultGeneration && workload.State == domain.StateStarting:
				operation := domain.WorkloadOperation{
					WorkloadID: workload.ID, OperationKey: command.OperationKey,
					Operation: domain.OperationRestart, RequestDigest: digest,
					ResultGeneration: stored.ResultGeneration,
				}
				if err := m.driveLaunch(ctx, workload, operation); err != nil {
					return domain.Workload{}, err
				}
				result, err := m.repository.Get(ctx, workload.ID)
				if errors.Is(err, ports.ErrStoreUnavailable) {
					return domain.Workload{}, domain.ErrUnavailable
				}
				return result, err
			case workload.Generation > stored.ResultGeneration || workload.Generation == stored.ResultGeneration:
				return domain.Workload{}, m.refuseAmbiguousRestartReplay(ctx, stored)
			}
		}
	} else {
		now := m.now()
		if err := m.persistOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ResultGeneration: targetGeneration, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
		}
	}
	if workload.State != domain.StateRunning && workload.State != domain.StateFailed {
		if err := m.persistOperation(context.WithoutCancel(ctx), domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ErrorKind: domain.ErrorUnsupported, ResultGeneration: workload.Generation,
			CreatedAt: m.now(), UpdatedAt: m.now(),
		}); err != nil {
			return domain.Workload{}, err
		}
		return domain.Workload{}, domain.ErrUnsupported
	}
	if targetGeneration != workload.Generation+1 {
		if err := m.persistOperation(context.WithoutCancel(ctx), domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ErrorKind: domain.ErrorPermanent, ResultGeneration: targetGeneration,
			CreatedAt: m.now(), UpdatedAt: m.now(),
		}); err != nil {
			return domain.Workload{}, err
		}
		return domain.Workload{}, domain.ErrCorrupt
	}
	if workload.RestartCount >= workload.Effective.RestartLimit {
		err := domain.ErrRestartLimitExhausted
		if recordErr := m.persistOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ErrorKind: domain.ErrorLimitExhausted, ResultGeneration: workload.Generation,
			CreatedAt: m.now(), UpdatedAt: m.now(),
		}); recordErr != nil {
			return domain.Workload{}, recordErr
		}
		return domain.Workload{}, err
	}
	// The deterministic name may not be reused until the exact, fully
	// labelled old generation is confirmed absent. A stale or foreign object
	// is never adopted or removed, and a transient cleanup failure does not
	// advance the durable generation.
	if err := m.removeWorkloadContainer(ctx, workload); err != nil {
		kind := domain.ErrorUnavailable
		if errors.Is(err, domain.ErrCorrupt) {
			kind = domain.ErrorPermanent
		}
		if recordErr := m.persistOperation(context.WithoutCancel(ctx), domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationRestart, RequestDigest: digest,
			ErrorKind: kind, ResultGeneration: targetGeneration,
			CreatedAt: m.now(), UpdatedAt: m.now(),
		}); recordErr != nil {
			return domain.Workload{}, recordErr
		}
		return domain.Workload{}, err
	}
	next := workload
	next.Generation = targetGeneration
	next.RestartCount = workload.RestartCount + 1
	next.State = domain.StateStarting
	// Keep the in-memory launch descriptor identical to the guarded durable
	// transition below. In particular, the old generation's container ID must
	// not make post-create identity verification reject the new exact object.
	next.ContainerID = ""
	next.Endpoint = ""
	next.CgroupPath = ""
	next.HealthVerdict = domain.HealthUnknown
	next.LastExit = domain.ExitNone
	next.BaselineOOM = 0
	next.BaselinePids = 0
	next.IdleSince = nil
	next.LastVerifiedAt = nil
	next.StartedAt = nil
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
		Operation: domain.OperationRestart, RequestDigest: digest, ResultGeneration: targetGeneration,
	}
	if err := m.driveLaunch(ctx, next, operation); err != nil {
		return domain.Workload{}, err
	}
	result, err := m.repository.Get(ctx, workload.ID)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.Workload{}, domain.ErrUnavailable
		}
		return domain.Workload{}, fmt.Errorf("restart workload: %w", err)
	}
	if result.State != domain.StateRunning {
		return domain.Workload{}, domain.ErrCorrupt
	}
	return result, nil
}

func (m *Manager) refuseAmbiguousRestartReplay(ctx context.Context, stored ports.StoredOperation) error {
	now := m.now()
	if err := m.persistOperation(context.WithoutCancel(ctx), domain.WorkloadOperation{
		WorkloadID: stored.WorkloadID, OperationKey: stored.OperationKey,
		Operation: stored.Operation, RequestDigest: stored.RequestDigest,
		ResultGeneration: stored.ResultGeneration, ErrorKind: domain.ErrorPermanent,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return err
	}
	return domain.ErrFailed
}

func (m *Manager) finalizeRestartReplay(ctx context.Context, workload domain.Workload, stored ports.StoredOperation) (domain.Workload, error) {
	now := m.now()
	if err := m.persistOperation(ctx, domain.WorkloadOperation{
		WorkloadID: stored.WorkloadID, OperationKey: stored.OperationKey,
		Operation: stored.Operation, RequestDigest: stored.RequestDigest,
		ResultState: domain.StateRunning, ResultGeneration: stored.ResultGeneration,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return domain.Workload{}, fmt.Errorf("finalize restart replay: %w", err)
	}
	workload.Generation = stored.ResultGeneration
	return workload, nil
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
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("terminate workload: %w", err)
	}
	if stored.OperationKey != "" {
		if stored.RequestDigest != digest {
			return domain.ErrIdempotencyConflict
		}
		if stored.Completed() && !stored.Retryable() {
			return replayWorkloadError(stored.ErrorKind)
		}
	} else {
		now := m.now()
		if err := m.persistOperation(ctx, domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationTerminate, RequestDigest: digest,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return fmt.Errorf("terminate workload: %w", err)
		}
	}
	now := m.now()
	if !workload.State.Terminal() && workload.State != domain.StateStopping {
		if err := m.repository.Transition(ctx, workload.ID, workload.State, domain.StateStopping, ports.WorkloadFacts{
			ContainerID: workload.ContainerID, Endpoint: workload.Endpoint, CgroupPath: workload.CgroupPath,
			Generation: workload.Generation, RestartCount: workload.RestartCount,
			HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
		}, now); err != nil && !errors.Is(err, domain.ErrNotFound) {
			if errors.Is(err, ports.ErrStoreUnavailable) {
				return domain.ErrUnavailable
			}
			return fmt.Errorf("terminate workload: %w", err)
		}
	}
	if err := m.removeOwnedWorkloadContainer(ctx, workload); err != nil {
		kind := domain.ErrorUnavailable
		if errors.Is(err, domain.ErrCorrupt) {
			kind = domain.ErrorPermanent
		}
		if recordErr := m.persistOperation(context.WithoutCancel(ctx), domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationTerminate, RequestDigest: digest,
			ErrorKind: kind, ResultGeneration: workload.Generation,
			CreatedAt: now, UpdatedAt: m.now(),
		}); recordErr != nil {
			return recordErr
		}
		return err
	}
	if workload.State.Terminal() {
		return m.recordStoppedOperation(ctx, workload, command.OperationKey, digest)
	}
	stoppedAt := m.now()
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationTerminate, RequestDigest: digest,
		ResultState: domain.StateStopped, ResultGeneration: workload.Generation,
		CreatedAt: stoppedAt, UpdatedAt: stoppedAt,
	}
	if err := m.repository.TransitionOperation(ctx, workload.ID, domain.StateStopping, domain.StateStopped, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
		StoppedAt: &stoppedAt, ClearEngine: true,
	}, operation, stoppedAt); err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("terminate workload: %w", err)
	}
	return nil
}

func (m *Manager) recordStoppedOperation(ctx context.Context, workload domain.Workload, operationKey, digest string) error {
	now := m.now()
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: operationKey,
		Operation: domain.OperationTerminate, RequestDigest: digest,
		ResultState: domain.StateStopped, ResultGeneration: workload.Generation,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := m.persistOperation(ctx, operation); err != nil {
		return fmt.Errorf("terminate workload: %w", err)
	}
	return nil
}

func (m *Manager) persistOperation(ctx context.Context, operation domain.WorkloadOperation) error {
	if err := m.repository.RecordOperation(ctx, operation); err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("persist workload operation: %w", err)
	}
	return nil
}

func (m *Manager) terminateDigest(workload domain.Workload, reason string) string {
	base := domain.OperationDigest(domain.OperationTerminate, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	sum := sha256.Sum256([]byte(base + "|" + reason))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// removeWorkloadContainer removes one exact, fully verified engine object.
// Absence is converged; a label/name/ID mismatch is corruption and is never
// touched. Success includes a final absence confirmation.
func (m *Manager) removeWorkloadContainer(ctx context.Context, workload domain.Workload) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	facts, err := m.inspectWorkloadContainer(cleanupCtx, workload)
	if err != nil {
		return err
	}
	if facts.ID == "" {
		return nil
	}
	if !matchesWorkloadContainer(workload, facts) {
		return domain.ErrCorrupt
	}
	return m.removeInspectedContainer(cleanupCtx, facts)
}

// removeOwnedWorkloadContainer is the shutdown convergence path. Exact
// persisted identity and ownership labels are sufficient authority to stop
// an object even when its immutable/security profile drifted; otherwise a
// hostile widening could strand a live container forever in stopping. The
// stricter removeWorkloadContainer remains the restart/adoption gate.
func (m *Manager) removeOwnedWorkloadContainer(ctx context.Context, workload domain.Workload) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	facts, err := m.inspectWorkloadContainer(cleanupCtx, workload)
	if err != nil {
		return err
	}
	if facts.ID == "" {
		return nil
	}
	if !matchesWorkloadIdentity(workload, facts) {
		return domain.ErrCorrupt
	}
	return m.removeInspectedContainer(cleanupCtx, facts)
}

func (m *Manager) inspectWorkloadContainer(ctx context.Context, workload domain.Workload) (ports.ContainerFacts, error) {
	target := workload.ContainerID
	if target == "" {
		target = workload.ContainerName
	}
	facts, err := m.engine.InspectContainer(ctx, target)
	if errors.Is(err, ports.ErrContainerNotFound) && target != workload.ContainerName {
		facts, err = m.engine.InspectContainer(ctx, workload.ContainerName)
	}
	if errors.Is(err, ports.ErrContainerNotFound) {
		return ports.ContainerFacts{}, nil
	}
	if err != nil {
		return ports.ContainerFacts{}, domain.ErrUnavailable
	}
	return facts, nil
}

func (m *Manager) removeInspectedContainer(ctx context.Context, facts ports.ContainerFacts) error {
	if err := m.engine.StopContainer(ctx, facts.ID, 10*time.Second); err != nil && !errors.Is(err, ports.ErrContainerNotFound) {
		return domain.ErrUnavailable
	}
	if err := m.engine.RemoveContainer(ctx, facts.ID); err != nil && !errors.Is(err, ports.ErrContainerNotFound) {
		return domain.ErrUnavailable
	}
	if _, err := m.engine.InspectContainer(ctx, facts.ID); err == nil {
		return domain.ErrUnavailable
	} else if !errors.Is(err, ports.ErrContainerNotFound) {
		return domain.ErrUnavailable
	}
	return nil
}

func matchesWorkloadContainer(workload domain.Workload, facts ports.ContainerFacts) bool {
	if !matchesWorkloadIdentity(workload, facts) {
		return false
	}
	if facts.Image != workload.Image || len(facts.Command) != len(workload.Command) {
		return false
	}
	for index := range workload.Command {
		if facts.Command[index] != workload.Command[index] {
			return false
		}
	}
	tmpfs := facts.Tmpfs["/tmp"]
	return facts.PublishedPorts == 1 && int64(facts.ContainerPort) == workload.Port &&
		facts.HostIP == "127.0.0.1" && facts.HostPort > 0 && facts.HostPort <= 65535 &&
		facts.ReadOnly && !facts.Privileged && facts.CapabilitiesAdded == 0 &&
		facts.EffectiveCapabilities == 0 && facts.BoundingCapabilities == 0 &&
		facts.NoNewPrivileges && facts.UnexpectedSecurityOpts == 0 && !facts.AutoRemove &&
		facts.NetworkMode == "workos-app-internal" && facts.ConnectedNetworks == 1 && facts.InternalNetwork &&
		facts.RestartPolicy == "no" && facts.BindMounts == 0 && facts.UnexpectedMounts == 0 && facts.Devices == 0 &&
		len(facts.Tmpfs) == 1 && matchesWorkloadTmpfs(tmpfs)
}

func matchesWorkloadTmpfs(options string) bool {
	required := map[string]bool{
		"rw": false, "size=33554432": false, "noexec": false, "nodev": false, "nosuid": false,
	}
	parts := strings.Split(options, ",")
	if len(parts) != len(required) {
		return false
	}
	for _, option := range parts {
		if _, ok := required[option]; !ok || required[option] {
			return false
		}
		required[option] = true
	}
	return true
}

func matchesWorkloadIdentity(workload domain.Workload, facts ports.ContainerFacts) bool {
	if facts.ID == "" || facts.Name != workload.ContainerName {
		return false
	}
	if workload.ContainerID != "" && facts.ID != workload.ContainerID {
		return false
	}
	for key, value := range domain.EngineLabels(workload) {
		if facts.Labels[key] != value {
			return false
		}
	}
	return true
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
	case domain.ErrorUnavailable:
		return domain.ErrUnavailable
	case domain.ErrorPermanent, domain.ErrorFailed:
		return domain.ErrFailed
	default:
		return nil
	}
}
