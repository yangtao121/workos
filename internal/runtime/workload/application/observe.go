package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// maxObservations bounds one observation sweep.
const maxObservations = 1000

// LookupRunning returns the workload only when it is running with the exact
// expected generation; any drift, terminal state, or miss fails closed with
// ErrNotFound. Store uncertainty remains ErrUnavailable so callers never
// misreport an outage as an absent workload. This is the web-service
// session's launch-target verdict.
func (m *Manager) LookupRunning(ctx context.Context, workloadID string, generation int64) (domain.Workload, error) {
	if !domain.ValidWorkloadID(workloadID) || generation < 1 {
		return domain.Workload{}, domain.ErrNotFound
	}
	workload, err := m.repository.Get(ctx, workloadID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalid) {
			return domain.Workload{}, domain.ErrNotFound
		}
		return domain.Workload{}, domain.ErrUnavailable
	}
	if workload.State != domain.StateRunning || workload.Generation != generation ||
		!domain.ValidLoopbackEndpoint(workload.Endpoint) || workload.CgroupPath == "" {
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
		if workload.State == domain.StateRunning {
			if workload.CgroupPath == "" || !domain.ValidLoopbackEndpoint(workload.Endpoint) {
				return nil, domain.ErrUnavailable
			}
			counters, err := m.cgroup.ReadCounters(ctx, workload.CgroupPath)
			if err != nil {
				// Missing or malformed kernel counters are unavailable evidence,
				// never a fabricated all-zero observation.
				return nil, domain.ErrUnavailable
			}
			observation.CPUUsageUSec = counters.CPUUsageUSec
			observation.MemoryCurrent = counters.MemoryCurrent
			observation.MemoryPeak = counters.MemoryPeak
			observation.MemoryOOMs = counters.MemoryOOMs
			observation.PIDsCurrent = counters.PIDsCurrent
			observation.PIDsLimitEvents = counters.PIDsLimitEvents
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
			} else if counters.PIDsLimitEvents > workload.BaselinePids {
				observation.ExitCategory = domain.ExitPIDs
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
			// Also converge legacy/crash-window engine objects left behind a
			// terminal row. Identity verification ensures a foreign object is
			// never touched.
			if err := m.removeOwnedWorkloadContainer(ctx, workload); err != nil && !errors.Is(err, domain.ErrCorrupt) {
				m.logger.Info("workload terminal cleanup pending", "error", err)
			}
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
	if workload.State == domain.StatePending {
		now := m.now()
		if err := m.repository.Transition(ctx, workload.ID, domain.StatePending, domain.StateStarting, ports.WorkloadFacts{
			Generation: workload.Generation, RestartCount: workload.RestartCount,
			HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone,
		}, now); err != nil {
			m.logger.Info("workload reconcile pending transition", "error", err)
			return
		}
		workload.State = domain.StateStarting
		workload.UpdatedAt = now
	}
	stored, err := m.repository.PendingOperation(ctx, workload.ID, workload.Generation)
	if err != nil {
		m.logger.Info("workload reconcile operation lookup pending", "error", err)
		return
	}
	operation := domain.WorkloadOperation{}
	if stored.OperationKey != "" {
		operation = domain.WorkloadOperation{
			WorkloadID: stored.WorkloadID, OperationKey: stored.OperationKey,
			Operation: stored.Operation, RequestDigest: stored.RequestDigest,
			ResultGeneration: stored.ResultGeneration,
		}
	} else {
		operation = domain.WorkloadOperation{
			WorkloadID: workload.ID, OperationKey: "reconcile:" + fmt.Sprintf("%d", workload.Generation),
			Operation: domain.OperationEnsure, RequestDigest: domain.OperationDigest(
				domain.OperationEnsure, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested),
			ResultGeneration: workload.Generation,
		}
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
	if !matchesWorkloadIdentity(workload, facts) {
		// Never touch a foreign container. The durable row fails closed and the
		// mismatched object remains outside WorkOS cleanup authority.
		m.failRunning(ctx, workload, domain.ExitUnknown)
		return
	}
	if !matchesWorkloadContainer(workload, facts) {
		m.failOwnedRunning(ctx, workload, facts, domain.ExitUnknown)
		return
	}
	if !facts.Running {
		category := domain.ExitExited
		if facts.OOMKilled {
			category = domain.ExitOOM
		}
		if err := m.removeWorkloadContainer(ctx, workload); err != nil {
			m.logger.Info("workload exited-container cleanup pending", "error", err)
			return
		}
		m.failRunning(ctx, workload, category)
		return
	}
	expectedEndpoint := "127.0.0.1:" + strconv.Itoa(int(facts.HostPort))
	cgroupPath, cgroupErr := m.resolveCgroup(probeCtx, facts.PID)
	if cgroupErr != nil || expectedEndpoint != workload.Endpoint || cgroupPath != workload.CgroupPath {
		m.failOwnedRunning(ctx, workload, facts, domain.ExitUnknown)
		return
	}
	effective, effectiveErr := m.cgroup.ReadEffective(probeCtx, cgroupPath)
	if effectiveErr != nil || !matchesEffectivePolicy(workload, effective) {
		m.failOwnedRunning(ctx, workload, facts, domain.ExitUnknown)
		return
	}
	// Core re-validation under the workload owner's trusted identity (the
	// private Core client requires the owner/device pair; the device is this
	// runtime's own service identity). Definitive uninstalled/archived stops
	// immediately; transient Core outages burn the bounded grace before the
	// fail-safe stop — hard limits keep enforcing while the grace burns.
	verifyCtx := identity.WithContext(probeCtx, identity.Identity{
		UserID: workload.OwnerUserID, DeviceID: m.config.VerifyDeviceID,
	})
	verdict, verifyErr := m.verifier.VerifyLaunch(verifyCtx, ports.LaunchQuery{
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
	default:
		// The grace is measured from the last successful verification (the
		// launch itself anchors it); a pre-stamp legacy row falls back to its
		// creation time so a nil stamp can never dodge the fail-safe.
		anchor := workload.CreatedAt
		if workload.LastVerifiedAt != nil {
			anchor = *workload.LastVerifiedAt
		}
		if now.Sub(anchor) > m.config.CoreGrace {
			_ = m.Terminate(ctx, ports.TerminateCommand{
				WorkloadID: workload.ID, OperationKey: "reconcile:fail-safe", Reason: "fail_safe",
			})
		}
	}
	// Idle TTL is anchored to the durable beginning of the current no-surface
	// interval, never a lifecycle/update timestamp. A newly idle long-lived
	// workload therefore gets a full TTL.
	hasSurface, surfaceErr := m.surfaces.HasActiveSurface(ctx, workload.OwnerUserID, workload.AppInstanceID)
	if surfaceErr != nil {
		return
	}
	if hasSurface {
		_, _ = m.repository.SetIdle(ctx, workload.ID, workload.Generation, false, now)
		return
	}
	idleSince, idleErr := m.repository.SetIdle(ctx, workload.ID, workload.Generation, true, now)
	if idleErr == nil && idleSince != nil && now.Sub(*idleSince) > m.config.IdleTTL {
		_ = m.Terminate(ctx, ports.TerminateCommand{
			WorkloadID: workload.ID, OperationKey: "reconcile:idle", Reason: "idle",
		})
	}
}

// failOwnedRunning removes an exact persisted container identity before
// closing the row. Unlike an identity mismatch, immutable/security/cgroup
// drift is still an object this workload unquestionably owns, so leaving it
// live would escape the state machine.
func (m *Manager) failOwnedRunning(ctx context.Context, workload domain.Workload, facts ports.ContainerFacts, category string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := m.removeInspectedContainer(cleanupCtx, facts); err != nil {
		m.logger.Info("workload drift cleanup pending", "error", err)
		return
	}
	m.failRunning(ctx, workload, category)
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
	if err := m.removeOwnedWorkloadContainer(ctx, workload); err != nil {
		m.logger.Info("workload reconcile stop cleanup pending", "error", err)
		return
	}
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
		if !domain.ValidWorkloadID(id) {
			continue
		}
		if _, ok := known[id]; ok {
			continue
		}
		if _, err := m.repository.Get(sweepCtx, id); errors.Is(err, domain.ErrNotFound) {
			generation, parseErr := strconv.ParseInt(facts.Labels["workos.workload.generation"], 10, 64)
			if parseErr != nil || generation < 1 {
				continue
			}
			orphan := domain.Workload{
				ID: id, OwnerUserID: facts.Labels["workos.owner"],
				AppInstanceID: facts.Labels["workos.workload.instance"],
				Generation:    generation, ContainerID: facts.ID, ContainerName: facts.Name,
			}
			if !domain.ValidUUIDv7(orphan.OwnerUserID) || !domain.ValidUUIDv7(orphan.AppInstanceID) ||
				!matchesWorkloadIdentity(orphan, facts) {
				continue
			}
			m.logger.Info("workload orphan removal")
			_ = m.removeInspectedContainer(sweepCtx, facts)
		}
	}
}
