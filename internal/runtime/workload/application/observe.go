package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// maxObservations bounds one observation sweep.
const maxObservations = 1000

// LookupRunning returns the workload only when it is running with the exact
// expected generation; any drift, terminal state, or miss fails closed with
// ErrNotFound. This is the web-service session's launch-target verdict.
func (m *Manager) LookupRunning(ctx context.Context, workloadID string, generation int64) (domain.Workload, error) {
	if !domain.ValidWorkloadID(workloadID) || generation < 1 {
		return domain.Workload{}, domain.ErrNotFound
	}
	workload, err := m.repository.Get(ctx, workloadID)
	if err != nil {
		return domain.Workload{}, domain.ErrNotFound
	}
	if workload.State != domain.StateRunning || workload.Generation != generation ||
		workload.Endpoint == "" {
		return domain.Workload{}, domain.ErrNotFound
	}
	return workload, nil
}

// Observe returns the neutral, bounded observation snapshot of every
// supervised workload. Running workloads get a live health verdict, real
// cgroup counters, and a bounded exit classification; every other state
// reports the persisted facts. The output never carries host endpoints,
// cgroup paths, container IDs, or any content.
func (m *Manager) Observe(ctx context.Context) ([]ports.Observation, error) {
	workloads, err := m.repository.List(ctx, maxObservations)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return nil, domain.ErrUnavailable
		}
		return nil, fmt.Errorf("observe workloads: %w", err)
	}
	now := m.now()
	observations := make([]ports.Observation, 0, len(workloads))
	for _, workload := range workloads {
		observation := ports.Observation{
			WorkloadID: workload.ID, OwnerUserID: workload.OwnerUserID,
			ProjectID: workload.ProjectID, AppInstanceID: workload.AppInstanceID,
			AppID: workload.AppID, ManifestDigest: workload.ManifestDigest,
			Generation: workload.Generation, State: workload.State,
			RestartCount:  workload.RestartCount,
			HealthVerdict: workload.HealthVerdict, ExitCategory: workload.LastExit,
			ObservedAt: now,
		}
		observation.Idle = m.isIdleInternal(ctx, workload)
		if workload.State == domain.StateRunning && workload.CgroupPath != "" {
			counters, err := m.cgroup.ReadCounters(ctx, workload.CgroupPath)
			if err == nil {
				observation.CPUUsageUSec = counters.CPUUsageUSec
				observation.MemoryCurrent = counters.MemoryCurrent
				observation.MemoryPeak = counters.MemoryPeak
				observation.MemoryOOMs = counters.MemoryOOMs
				observation.PIDsCurrent = counters.PIDsCurrent
				observation.PIDsPeak = counters.PIDsPeak
			}
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			result, probeErr := m.prober.Probe(probeCtx, workload.Endpoint, workload.Effective.HealthPath, time.Second)
			cancel()
			if probeErr == nil {
				observation.HealthVerdict = result.Verdict
			} else {
				observation.HealthVerdict = domain.HealthFailing
			}
			// OOM detection: the cgroup oom counter above this generation's
			// baseline is a bounded, numeric fact.
			if counters.MemoryOOMs > workload.BaselineOOM {
				observation.ExitCategory = domain.ExitOOM
			}
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (m *Manager) isIdleInternal(ctx context.Context, workload domain.Workload) bool {
	if workload.State != domain.StateRunning {
		return false
	}
	hasSurface, err := m.surfaces.HasActiveSurface(ctx, workload.OwnerUserID, workload.AppInstanceID)
	if err != nil {
		// Uncertainty is conservative: never idle-stop on a failed lookup.
		return false
	}
	return !hasSurface
}

// Reconcile converges the durable rows and the engine objects: it re-drives
// interrupted launches and terminations, fails exited workloads with a
// bounded exit classification, re-validates installations through Core,
// enforces the idle TTL, and removes exactly the labeled orphans this runtime
// created without a surviving row. It is deterministic, bounded, and safe to
// run concurrently with the public commands.
func (m *Manager) Reconcile(ctx context.Context) error {
	workloads, err := m.repository.List(ctx, maxObservations)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("reconcile workloads: %w", err)
	}
	now := m.now()
	known := make(map[string]struct{}, len(workloads))
	for _, workload := range workloads {
		known[workload.ID] = struct{}{}
		if !m.claimLease(ctx, workload.ID, now) {
			continue
		}
		switch workload.State {
		case domain.StatePending, domain.StateStarting:
			// An interrupted ensure/restart: re-drive the same deterministic
			// sequence. A missing operation row means the reserve transaction
			// never committed, so the convergence below re-creates one.
			m.reconcileStarting(ctx, workload)
		case domain.StateRunning:
			m.reconcileRunning(ctx, workload, now)
		case domain.StateStopping:
			m.reconcileStopping(ctx, workload)
		default:
			// Terminal rows keep no engine object; nothing to drive.
		}
	}
	m.removeOrphans(ctx, known)
	return nil
}

func (m *Manager) claimLease(ctx context.Context, workloadID string, now time.Time) bool {
	claimed, err := m.repository.ClaimLease(ctx, workloadID, m.config.InstanceName, now.Add(m.config.LeaseTTL))
	if err != nil || !claimed {
		return false
	}
	return true
}

func (m *Manager) reconcileStarting(ctx context.Context, workload domain.Workload) {
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: "reconcile:" + fmt.Sprintf("%d", workload.Generation),
		Operation: domain.OperationEnsure, RequestDigest: domain.OperationDigest(
			domain.OperationEnsure, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested),
	}
	if err := m.driveLaunch(ctx, workload, operation); err != nil {
		m.logger.Info("workload reconcile launch re-drive", "error", err)
	}
}

func (m *Manager) reconcileRunning(ctx context.Context, workload domain.Workload, now time.Time) {
	probeCtx, cancel := context.WithTimeout(ctx, m.config.OperationTimeout)
	defer cancel()
	facts, err := m.engine.InspectContainer(probeCtx, workload.ContainerName)
	if errors.Is(err, ports.ErrContainerNotFound) {
		// The engine object vanished: an honest runtime failure, bounded and
		// classified. The supervisor decides what happens next.
		m.failRunning(ctx, workload, domain.ExitUnknown)
		return
	}
	if err != nil {
		m.logger.Info("workload reconcile inspect unavailable", "error", err)
		return
	}
	if facts.Labels["workos.workload.id"] != workload.ID {
		// Never touch a foreign container; the drift is corrupt.
		m.failRunning(ctx, workload, domain.ExitUnknown)
		return
	}
	if !facts.Running {
		category := domain.ExitExited
		if facts.OOMKilled {
			category = domain.ExitOOM
		}
		m.removeContainer(ctx, facts.ID, workload.ContainerName)
		m.failRunning(ctx, workload, category)
		return
	}
	// Core re-validation: definitive uninstalled/archived stops immediately;
	// transient Core outages burn the bounded grace before the fail-safe
	// stop — hard limits keep enforcing while the grace burns.
	verdict, verifyErr := m.verifier.VerifyLaunch(probeCtx, ports.LaunchQuery{
		OwnerUserID: workload.OwnerUserID, ProjectID: workload.ProjectID,
		AppInstanceID: workload.AppInstanceID, ManifestDigest: workload.ManifestDigest,
	})
	switch {
	case verifyErr == nil && verdict == ports.LaunchGone:
		_ = m.Terminate(ctx, ports.TerminateCommand{
			WorkloadID: workload.ID, OperationKey: "reconcile:uninstalled", Reason: "uninstalled",
		})
		return
	case verifyErr == nil && verdict == ports.LaunchInstalled:
		verifiedAt := now
		_ = m.repository.Transition(ctx, workload.ID, domain.StateRunning, domain.StateRunning, ports.WorkloadFacts{
			Generation: workload.Generation, RestartCount: workload.RestartCount,
			HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
			VerifiedAt: &verifiedAt,
		}, now)
	case verifyErr != nil || verdict == ports.LaunchUnknown:
		if workload.LastVerifiedAt != nil && now.Sub(*workload.LastVerifiedAt) > m.config.CoreGrace {
			_ = m.Terminate(ctx, ports.TerminateCommand{
				WorkloadID: workload.ID, OperationKey: "reconcile:fail-safe", Reason: "fail_safe",
			})
		}
	}
	// Idle TTL: a running workload with no open surface session for longer
	// than the bounded TTL is deterministically stopped. The next Open
	// ensures a fresh launch under a fresh key.
	if m.isIdleInternal(ctx, workload) && now.Sub(workload.UpdatedAt) > m.config.IdleTTL {
		_ = m.Terminate(ctx, ports.TerminateCommand{
			WorkloadID: workload.ID, OperationKey: "reconcile:idle", Reason: "idle",
		})
	}
}

func (m *Manager) failRunning(ctx context.Context, workload domain.Workload, category string) {
	now := m.now()
	if err := m.repository.Transition(ctx, workload.ID, domain.StateRunning, domain.StateFailed, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: domain.HealthFailing, LastExit: category,
		StoppedAt: &now, ClearEngine: true,
	}, now); err != nil {
		m.logger.Warn("workload reconcile failure transition incomplete", "error", err)
	}
}

func (m *Manager) reconcileStopping(ctx context.Context, workload domain.Workload) {
	m.removeContainer(ctx, workload.ContainerID, workload.ContainerName)
	stoppedAt := m.now()
	if err := m.repository.Transition(ctx, workload.ID, domain.StateStopping, domain.StateStopped, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
		StoppedAt: &stoppedAt, ClearEngine: true,
	}, stoppedAt); err != nil {
		m.logger.Info("workload reconcile stop convergence pending", "error", err)
	}
}

// removeOrphans removes exactly the containers that carry the full WorkOS
// identity labels but whose workload row is gone (a scratch-DB reset is the
// only path that can leave one). Unlabeled or foreign containers are never
// listed, never adopted, and never removed.
func (m *Manager) removeOrphans(ctx context.Context, known map[string]struct{}) {
	sweepCtx, cancel := context.WithTimeout(ctx, m.config.OperationTimeout)
	defer cancel()
	managed, err := m.engine.ListManagedContainers(sweepCtx)
	if err != nil {
		return
	}
	for _, facts := range managed {
		id := facts.Labels["workos.workload.id"]
		if _, ok := known[id]; ok {
			continue
		}
		if _, err := m.repository.Get(sweepCtx, id); errors.Is(err, domain.ErrNotFound) {
			m.logger.Info("workload orphan removal", "error", nil)
			m.removeContainer(sweepCtx, facts.ID, facts.Name)
		}
	}
}
