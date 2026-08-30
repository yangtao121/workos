// Package application holds the Workload Manager's use cases: durable
// idempotent ensure/restart/terminate commands over a verified rootless
// engine, neutral observations, and the deterministic reconciliation that
// converges every crash window between the database and the engine
// (ADR-0006 §4).
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// Config bounds the manager's timers. All of them are validated at
// construction so a misconfigured runtime refuses to start instead of
// guessing.
type Config struct {
	// ReconcileInterval is the periodic convergence cadence.
	ReconcileInterval time.Duration
	// IdleTTL is how long a running workload without any open surface
	// session survives before the deterministic idle stop.
	IdleTTL time.Duration
	// OperationTimeout bounds one engine side-effect sequence.
	OperationTimeout time.Duration
	// CoreGrace bounds how long a running workload survives without a
	// successful Core re-validation before the fail-safe stop.
	CoreGrace time.Duration
	// LeaseTTL is the reconcile lease window.
	LeaseTTL time.Duration
	// InstanceName identifies this runtime instance in leases.
	InstanceName string
	// VerifyDeviceID is the runtime's own service device identity, paired
	// with each workload's owner for the private Core re-validation calls
	// the reconcile loop makes.
	VerifyDeviceID string
}

func (c Config) validate() error {
	if c.ReconcileInterval < time.Second || c.ReconcileInterval > time.Hour {
		return errors.New("workload reconcile interval must be between 1s and 1h")
	}
	if c.IdleTTL < 30*time.Second || c.IdleTTL > 24*time.Hour {
		return errors.New("workload idle TTL must be between 30s and 24h")
	}
	if c.OperationTimeout < 5*time.Second || c.OperationTimeout > 10*time.Minute {
		return errors.New("workload operation timeout must be between 5s and 10m")
	}
	if c.CoreGrace < 30*time.Second || c.CoreGrace > time.Hour {
		return errors.New("workload Core grace must be between 30s and 1h")
	}
	if c.LeaseTTL < 2*time.Second || c.LeaseTTL > time.Hour {
		return errors.New("workload lease TTL must be between 2s and 1h")
	}
	if c.InstanceName == "" || len(c.InstanceName) > 128 {
		return errors.New("workload instance name is required")
	}
	if !domain.ValidUUIDv7(c.VerifyDeviceID) {
		return errors.New("workload verify device identity must be a canonical UUIDv7")
	}
	return nil
}

// Manager owns the supervised workload lifecycle. Every public command is
// durable and idempotent; every engine side effect is recoverable by
// Reconcile; no engine failure ever fabricates a running result.
type Manager struct {
	repository ports.WorkloadRepository
	engine     ports.Engine
	cgroup     ports.CgroupReader
	prober     ports.HealthProber
	verifier   ports.InstallationVerifier
	surfaces   ports.SurfaceReferenceSource
	ids        ids.Generator
	config     Config
	now        func() time.Time
	logger     *slog.Logger

	capability ports.Capability
}

func New(
	repository ports.WorkloadRepository,
	engine ports.Engine,
	cgroup ports.CgroupReader,
	prober ports.HealthProber,
	verifier ports.InstallationVerifier,
	surfaces ports.SurfaceReferenceSource,
	generator ids.Generator,
	config Config,
	logger *slog.Logger,
) (*Manager, error) {
	switch {
	case repository == nil, engine == nil, cgroup == nil, prober == nil, verifier == nil, surfaces == nil, generator == nil:
		return nil, errors.New("workload manager requires repository, engine, cgroup reader, health prober, installation verifier, surface source, and id generator")
	case logger == nil:
		return nil, errors.New("workload manager requires a logger")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Manager{
		repository: repository, engine: engine, cgroup: cgroup, prober: prober,
		verifier: verifier, surfaces: surfaces, ids: generator,
		config: config, now: func() time.Time { return time.Now().UTC() }, logger: logger,
	}, nil
}

// ProbeRunner verifies the host's rootless container capability once per
// process and caches the verdict. An unavailable capability is a hard,
// honest refusal: container surfaces report failed precondition and the
// system health reports the runner unavailable.
func (m *Manager) ProbeRunner(ctx context.Context) (ports.Capability, error) {
	capability, err := m.engine.Probe(ctx)
	if err != nil {
		return ports.Capability{Available: false,
			Reason: "container engine probe failed"}, nil
	}
	m.capability = capability
	return capability, nil
}

// RunnerStatus returns the cached capability (probing if not yet done).
func (m *Manager) RunnerStatus(ctx context.Context) ports.Capability {
	if m.capability.Reason == "" && !m.capability.Available {
		if capability, err := m.ProbeRunner(ctx); err == nil {
			return capability
		}
	}
	return m.capability
}

// Ensure returns the active workload of the installed instance, launching the
// container when no live one exists. Same key + same canonical descriptor
// replays the first result across restarts; a different descriptor under the
// same key is a stable conflict. Failures never fabricate a running result
// and never create unbounded orphans: reconciliation converges any partial
// side effect.
func (m *Manager) Ensure(ctx context.Context, command ports.EnsureCommand) (domain.Workload, error) {
	if !m.validEnsure(command) {
		return domain.Workload{}, domain.ErrInvalid
	}
	if !m.RunnerStatus(ctx).Available {
		return domain.Workload{}, domain.ErrRunnerUnavailable
	}
	existing, err := m.repository.GetActiveByInstance(ctx, command.OwnerUserID, command.AppInstanceID)
	switch {
	case err == nil:
		if existing.ManifestDigest != command.ManifestDigest {
			return domain.Workload{}, domain.ErrIdempotencyConflict
		}
		return m.ensureExisting(ctx, existing, command)
	case errors.Is(err, domain.ErrNotFound):
		// fall through to reserve
	case errors.Is(err, ports.ErrStoreUnavailable):
		return domain.Workload{}, domain.ErrUnavailable
	default:
		return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
	}
	if err := m.verifyImage(ctx, command.Image); err != nil {
		return domain.Workload{}, err
	}
	now := m.now()
	workload := domain.Workload{
		ID: m.ids.New(), OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		AppInstanceID: command.AppInstanceID, AppID: command.AppID, AppVersion: command.AppVersion,
		ManifestDigest: command.ManifestDigest, Image: command.Image, Command: append([]string(nil), command.Command...),
		Port: command.Port, Requested: command.Requested,
		Effective:  domain.EffectiveFromRequested(command.Requested),
		Generation: 1, State: domain.StateStarting, RestartCount: 0,
		ContainerName: domain.ContainerName(m.ids.New()),
		HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone,
		CreatedAt: now, UpdatedAt: now,
	}
	// The container name must be a pure function of the workload ID, fixed
	// before the reserve transaction so crash recovery converges on the same
	// engine object.
	workload.ContainerName = domain.ContainerName(workload.ID)
	digest := domain.OperationDigest(domain.OperationEnsure, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	operation := domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationEnsure, RequestDigest: digest,
		CreatedAt: now, UpdatedAt: now,
	}
	reserved, err := m.repository.ReserveEnsure(ctx, workload, operation)
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return domain.Workload{}, domain.ErrUnavailable
		}
		return domain.Workload{}, fmt.Errorf("reserve workload: %w", err)
	}
	if !reserved {
		// A concurrent ensure won the active slot or this key was consumed
		// earlier: classify by the stored facts instead of racing the engine.
		existing, err := m.repository.GetActiveByInstance(ctx, command.OwnerUserID, command.AppInstanceID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return domain.Workload{}, domain.ErrIdempotencyConflict
			}
			return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
		}
		if existing.ManifestDigest != command.ManifestDigest {
			return domain.Workload{}, domain.ErrIdempotencyConflict
		}
		return m.ensureExisting(ctx, existing, command)
	}
	if err := m.driveLaunch(ctx, workload, operation); err != nil {
		return domain.Workload{}, err
	}
	stored, err := m.repository.Get(ctx, workload.ID)
	if err != nil {
		return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
	}
	if stored.State != domain.StateRunning {
		return domain.Workload{}, domain.ErrCorrupt
	}
	return stored, nil
}

func (m *Manager) validEnsure(command ports.EnsureCommand) bool {
	return domain.ValidUUIDv7(command.OwnerUserID) && domain.ValidUUIDv7(command.ProjectID) &&
		domain.ValidUUIDv7(command.AppInstanceID) && domain.ValidOperationKey(command.OperationKey) &&
		domain.ValidDescriptor(command.AppID, command.AppVersion, command.ManifestDigest,
			command.Image, command.Command, command.Port, command.Requested)
}

func (m *Manager) ensureExisting(ctx context.Context, existing domain.Workload, command ports.EnsureCommand) (domain.Workload, error) {
	digest := domain.OperationDigest(domain.OperationEnsure, existing.ID, existing.Image, existing.Command, existing.Port, existing.Requested)
	stored, err := m.repository.LookupOperation(ctx, existing.ID, command.OperationKey)
	if err != nil {
		return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
	}
	if stored.OperationKey != "" {
		if stored.RequestDigest != digest {
			return domain.Workload{}, domain.ErrIdempotencyConflict
		}
		if stored.Completed() && !stored.Retryable() {
			if existing.State != domain.StateRunning {
				// A terminal verdict replayed against a non-running workload:
				// the workload is gone (stopped/failed) and only a fresh key
				// may start it again. The recorded verdict still replays.
				return existing, nil
			}
			return existing, nil
		}
	} else {
		now := m.now()
		if err := m.repository.RecordOperation(ctx, domain.WorkloadOperation{
			WorkloadID: existing.ID, OperationKey: command.OperationKey,
			Operation: domain.OperationEnsure, RequestDigest: digest,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
		}
	}
	if existing.State == domain.StateRunning {
		// The workload is live with the exact pinned descriptor: the ensure
		// attaches to it. No engine sequence runs, so a replay of an open
		// surface never disturbs the running container.
		return existing, nil
	}
	operation := domain.WorkloadOperation{
		WorkloadID: existing.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationEnsure, RequestDigest: digest,
	}
	if err := m.driveLaunch(ctx, existing, operation); err != nil {
		return domain.Workload{}, err
	}
	storedWorkload, err := m.repository.Get(ctx, existing.ID)
	if err != nil {
		return domain.Workload{}, fmt.Errorf("ensure workload: %w", err)
	}
	if storedWorkload.State != domain.StateRunning {
		return domain.Workload{}, domain.ErrCorrupt
	}
	return storedWorkload, nil
}

// verifyImage refuses any launch whose exact digest-pinned image is not
// already local. The engine is never asked to pull, log in, or resolve tags.
func (m *Manager) verifyImage(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, m.config.OperationTimeout)
	defer cancel()
	exists, err := m.engine.ImageExists(ctx, image)
	if err != nil {
		if errors.Is(err, ports.ErrEngineUnavailable) {
			return domain.ErrUnavailable
		}
		return fmt.Errorf("verify image: %w", err)
	}
	if !exists {
		return domain.ErrImageMissing
	}
	return nil
}

// driveLaunch performs the engine side-effect sequence for a workload in its
// starting window. Every step is idempotent given the deterministic container
// name and the persisted row: reconciliation re-runs the exact same sequence
// after any crash.
func (m *Manager) driveLaunch(ctx context.Context, workload domain.Workload, operation domain.WorkloadOperation) error {
	ctx, cancel := context.WithTimeout(ctx, m.config.OperationTimeout)
	defer cancel()

	if err := m.verifyImage(ctx, workload.Image); err != nil {
		return m.failLaunch(ctx, workload, operation, err)
	}
	// Converge the container object first: adopt a surviving container from a
	// crashed attempt, or create a fresh one under the deterministic name.
	facts, err := m.engine.InspectContainer(ctx, workload.ContainerName)
	switch {
	case err == nil:
		if facts.Labels["workos.workload.id"] != workload.ID {
			// The name is derived from the workload ID; a mismatch can only
			// be stored-fact corruption. Never adopt, never remove.
			return m.failLaunch(ctx, workload, operation, domain.ErrCorrupt)
		}
	case errors.Is(err, ports.ErrContainerNotFound):
		spec := ports.ContainerSpec{
			Name: workload.ContainerName, Image: workload.Image, Command: workload.Command,
			Port: workload.Port, Labels: domain.EngineLabels(workload), Policy: workload.Effective,
		}
		if _, err := m.engine.CreateContainer(ctx, spec); err != nil {
			return m.failLaunch(ctx, workload, operation, m.classifyEngine(err))
		}
	default:
		return m.failLaunch(ctx, workload, operation, m.classifyEngine(err))
	}
	if err := m.engine.StartContainer(ctx, workload.ContainerName); err != nil && !errors.Is(err, ports.ErrContainerNotFound) {
		return m.failLaunch(ctx, workload, operation, m.classifyEngine(err))
	}
	facts, err = m.engine.InspectContainer(ctx, workload.ContainerName)
	if err != nil {
		return m.failLaunch(ctx, workload, operation, m.classifyEngine(err))
	}
	if !facts.Running {
		// The container started and immediately exited: an honest, bounded
		// failure of the launch. failLaunch converges the engine object and
		// the row (non-retryable), classified from the exit facts.
		cause := domain.ErrFailed
		if facts.OOMKilled {
			cause = fmt.Errorf("%w: %s", domain.ErrFailed, domain.ExitOOM)
		}
		return m.failLaunch(ctx, workload, operation, cause)
	}
	// Verify the published endpoint is loopback-only before it is ever
	// persisted or served.
	if facts.HostIP != "127.0.0.1" || facts.HostPort < 1 || facts.HostPort > 65535 {
		return m.failLaunch(ctx, workload, operation, domain.ErrCorrupt)
	}
	endpoint := fmt.Sprintf("127.0.0.1:%d", facts.HostPort)
	// Resolve and validate the real cgroup path against this process's
	// delegated subtree, then read back the enforced limits. A configuration
	// that failed to apply stops the launch; it is never warned-and-continued.
	cgroupPath, err := m.resolveCgroup(ctx, facts.PID)
	if err != nil {
		return m.failLaunch(ctx, workload, operation, err)
	}
	effective, err := m.cgroup.ReadEffective(ctx, cgroupPath)
	if err != nil {
		return m.failLaunch(ctx, workload, operation, domain.ErrCorrupt)
	}
	if effective.CPUMaxUSec != workload.Effective.CPUQuotaUSec ||
		effective.MemoryHigh != workload.Effective.MemoryHighBytes ||
		effective.MemoryMax != workload.Effective.MemoryMaxBytes ||
		effective.PIDsMax != workload.Effective.PidsMax {
		return m.failLaunch(ctx, workload, operation, domain.ErrCorrupt)
	}
	// Startup health gate: no session is returned before the bounded probe
	// succeeds.
	if err := m.awaitStartupHealth(ctx, endpoint, workload.Effective.HealthPath, workload.Effective.StartupTimeout); err != nil {
		return m.failLaunch(ctx, workload, operation, err)
	}
	counters, _ := m.cgroup.ReadCounters(ctx, cgroupPath)
	now := m.now()
	startedAt := now
	if err := m.repository.Transition(ctx, workload.ID, domain.StateStarting, domain.StateRunning, ports.WorkloadFacts{
		ContainerID: facts.ID, Endpoint: endpoint, CgroupPath: cgroupPath,
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: domain.HealthOK, LastExit: domain.ExitNone,
		BaselineOOM: counters.MemoryOOMs, BaselinePids: counters.PIDsPeak,
		StartedAt: &startedAt,
	}, now); err != nil {
		return m.failLaunch(ctx, workload, operation, err)
	}
	operation.ResultState = domain.StateRunning
	operation.ResultGeneration = workload.Generation
	operation.UpdatedAt = now
	if err := m.repository.RecordOperation(ctx, operation); err != nil {
		return fmt.Errorf("finalize workload operation: %w", err)
	}
	// The launch succeeded against Core's freshest facts (the surface path
	// resolved moments ago): anchor the Core-grace clock here so a later
	// Core outage burns a bounded window from a real verification point.
	verifiedAt := now
	_ = m.repository.Transition(ctx, workload.ID, domain.StateRunning, domain.StateRunning, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: domain.HealthOK, LastExit: domain.ExitNone,
		VerifiedAt: &verifiedAt,
	}, now)
	return nil
}

// resolveCgroup derives the workload's host cgroup v2 path from the engine
// container's init PID and validates it against this process's delegated
// subtree. Traversal, empty paths, host/system cgroups, and subtree escapes
// are corrupt facts that stop the launch.
func (m *Manager) resolveCgroup(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", domain.ErrCorrupt
	}
	subtree, err := m.cgroup.SelfSubtree()
	if err != nil {
		return "", domain.ErrCorrupt
	}
	path, err := m.cgroup.CgroupPathForPID(pid)
	if err != nil {
		return "", domain.ErrCorrupt
	}
	if !domain.ValidCgroupPath(path, subtree) {
		return "", domain.ErrCorrupt
	}
	return path, nil
}

// awaitStartupHealth polls the health endpoint until the bounded startup
// window closes. A passing probe ends the launch; a timeout fails it.
func (m *Manager) awaitStartupHealth(ctx context.Context, endpoint, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		result, err := m.prober.Probe(ctx, endpoint, path, time.Second)
		if err == nil && result.Verdict == domain.HealthOK {
			return nil
		}
		if !time.Now().Before(deadline) {
			return domain.ErrFailed
		}
		if err := ctxSleep(ctx, 250*time.Millisecond); err != nil {
			return domain.ErrFailed
		}
	}
}

func ctxSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// failLaunch records the sanitized failure verdict for the operation and
// converges the workload row: the container side effect is removed
// best-effort and the row lands in failed with cleared engine facts. Retryable
// engine/store outages keep the starting row and a retryable op instead —
// reconciliation re-drives them; they never consume the key.
func (m *Manager) failLaunch(ctx context.Context, workload domain.Workload, operation domain.WorkloadOperation, cause error) error {
	classified := classify(cause)
	now := m.now()
	operation.ErrorKind = classified.kind
	operation.ResultGeneration = workload.Generation
	operation.UpdatedAt = now
	_ = m.repository.RecordOperation(ctx, operation)
	if classified.retryable {
		return cause
	}
	// Deterministic failure: converge the engine object and the row.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = m.engine.StopContainer(cleanupCtx, workload.ContainerName, 10*time.Second)
	_ = m.engine.RemoveContainer(cleanupCtx, workload.ContainerName)
	stoppedAt := now
	if err := m.repository.Transition(context.WithoutCancel(ctx), workload.ID, domain.StateStarting, domain.StateFailed, ports.WorkloadFacts{
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: domain.HealthFailing, LastExit: domain.ExitUnknown,
		StoppedAt: &stoppedAt, ClearEngine: true,
	}, now); err != nil {
		m.logger.Warn("workload fail-launch convergence incomplete", "error", err)
	}
	return cause
}

type failure struct {
	kind      domain.ErrorKind
	retryable bool
}

func classify(err error) failure {
	switch {
	case err == nil:
		return failure{}
	case errors.Is(err, domain.ErrUnavailable), errors.Is(err, ports.ErrEngineUnavailable), errors.Is(err, ports.ErrStoreUnavailable):
		return failure{kind: domain.ErrorUnavailable, retryable: true}
	case errors.Is(err, domain.ErrImageMissing), errors.Is(err, domain.ErrRunnerUnavailable), errors.Is(err, domain.ErrUnsupported):
		return failure{kind: domain.ErrorUnsupported, retryable: false}
	case errors.Is(err, domain.ErrRestartLimitExhausted):
		return failure{kind: domain.ErrorLimitExhausted, retryable: false}
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return failure{kind: domain.ErrorConflict, retryable: false}
	case errors.Is(err, domain.ErrInvalid):
		return failure{kind: domain.ErrorInvalid, retryable: false}
	case errors.Is(err, domain.ErrCorrupt), errors.Is(err, domain.ErrFailed):
		// Deterministic failures must converge: the engine object is
		// removed and the row lands in failed. Treating them as retryable
		// would leak a live (or drifted) container outside the state
		// machine's control.
		return failure{kind: domain.ErrorFailed, retryable: false}
	default:
		return failure{kind: domain.ErrorFailed, retryable: true}
	}
}

func (m *Manager) classifyEngine(err error) error {
	if errors.Is(err, ports.ErrEngineUnavailable) {
		return domain.ErrUnavailable
	}
	return err
}
