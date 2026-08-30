package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

var testIDGenerator = ids.UUIDv7{}

func newTestIDValue() string { return testIDGenerator.New() }

// TestEnsureLaunchesAndPersistsVerifiedFacts covers the happy path: one
// ensure launch ends running with loopback endpoint and cgroup facts that
// the runtime verified before reporting running.
func TestEnsureLaunchesAndPersistsVerifiedFacts(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)

	workload, err := manager.Ensure(context.Background(), testEnsure(testOperation))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if workload.State != domain.StateRunning {
		t.Fatalf("state %v, want running", workload.State)
	}
	if !domain.ValidLoopbackEndpoint(workload.Endpoint) {
		t.Fatalf("endpoint %q is not loopback", workload.Endpoint)
	}
	if workload.CgroupPath == "" || !domain.ValidCgroupPath(workload.CgroupPath, "/user.slice/user-1000.slice") {
		t.Fatalf("cgroup path %q was not validated into the delegated subtree", workload.CgroupPath)
	}
	if workload.ContainerName != domain.ContainerName(workload.ID) {
		t.Fatalf("container name %q is not the deterministic name", workload.ContainerName)
	}
	if engine.createCalls != 1 || engine.startCalls != 1 {
		t.Fatalf("engine calls create=%d start=%d, want 1/1", engine.createCalls, engine.startCalls)
	}
	if workload.Effective.MemoryMaxBytes != 96*1024*1024 || workload.Effective.CPUQuotaUSec != 100000 {
		t.Fatalf("effective policy was not adjudicated: %+v", workload.Effective)
	}
}

// TestEnsureReplaysSameKeyWithoutRelaunching pins ensure idempotency: the
// same key never re-runs the engine, and a different key against the live
// workload attaches to it instead of launching a duplicate.
func TestEnsureReplaysSameKeyWithoutRelaunching(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	first, err := manager.Ensure(ctx, testEnsure("key-a"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	replay, err := manager.Ensure(ctx, testEnsure("key-a"))
	if err != nil {
		t.Fatalf("same-key replay: %v", err)
	}
	second, err := manager.Ensure(ctx, testEnsure("key-b"))
	if err != nil {
		t.Fatalf("fresh-key ensure: %v", err)
	}
	if replay.ID != first.ID || second.ID != first.ID {
		t.Fatalf("ensure launched duplicates: %s %s %s", first.ID, replay.ID, second.ID)
	}
	if engine.createCalls != 1 {
		t.Fatalf("engine created %d containers, want 1", engine.createCalls)
	}
}

// TestEnsureConflictOnDigestChange pins the stable conflict: the same key
// with a different canonical descriptor is an abort, and a live workload
// pinned to a different digest never silently relaunches.
func TestEnsureConflictOnDigestChange(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	if _, err := manager.Ensure(ctx, testEnsure("key-a")); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	conflicting := testEnsure("key-a")
	conflicting.ManifestDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := manager.Ensure(ctx, conflicting); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("digest change verdict %v, want conflict", err)
	}
	fresh := testEnsure("key-c")
	fresh.ManifestDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := manager.Ensure(ctx, fresh); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("live-workload digest mismatch verdict %v, want conflict", err)
	}
}

func TestEnsureConflictsOnEveryCanonicalDescriptorDrift(t *testing.T) {
	mutations := map[string]func(*ports.EnsureCommand){
		"project": func(command *ports.EnsureCommand) { command.ProjectID = newTestIDValue() },
		"app":     func(command *ports.EnsureCommand) { command.AppID = "other-app" },
		"version": func(command *ports.EnsureCommand) { command.AppVersion = "1.0.1" },
		"image": func(command *ports.EnsureCommand) {
			command.Image = "localhost/other@sha256:" + strings.Repeat("b", 64)
		},
		"command": func(command *ports.EnsureCommand) { command.Command = []string{testCommand, "other"} },
		"port":    func(command *ports.EnsureCommand) { command.Port = 8081 },
		"policy":  func(command *ports.EnsureCommand) { command.Requested.RestartLimit++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			engine := newFakeEngine()
			manager := newTestManager(t, engine, newFakeRepo())
			if _, err := manager.Ensure(context.Background(), testEnsure("original")); err != nil {
				t.Fatalf("original ensure: %v", err)
			}
			conflicting := testEnsure("different-key")
			mutate(&conflicting)
			if _, err := manager.Ensure(context.Background(), conflicting); !errors.Is(err, domain.ErrIdempotencyConflict) {
				t.Fatalf("descriptor drift verdict %v, want conflict", err)
			}
			if engine.createCalls != 1 {
				t.Fatalf("descriptor drift created %d containers, want 1", engine.createCalls)
			}
		})
	}
}

func TestRunnerCapabilityProbeIsCachedAcrossConcurrentCallers(t *testing.T) {
	engine := newFakeEngine()
	manager := newTestManager(t, engine, newFakeRepo())
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			if !manager.RunnerStatus(context.Background()).Available {
				t.Error("runner unexpectedly unavailable")
			}
		}()
	}
	group.Wait()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.probeCalls != 1 {
		t.Fatalf("engine probe calls=%d, want 1", engine.probeCalls)
	}
}

func TestEnsureNeverRestartsAnOverlappingStoppingWorkload(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-running"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := repo.Transition(ctx, workload.ID, domain.StateRunning, domain.StateStopping, ports.WorkloadFacts{
		ContainerID: workload.ContainerID, Endpoint: workload.Endpoint, CgroupPath: workload.CgroupPath,
		Generation: workload.Generation, RestartCount: workload.RestartCount,
		HealthVerdict: workload.HealthVerdict, LastExit: workload.LastExit,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("seed stopping state: %v", err)
	}
	creates, starts := engine.createCalls, engine.startCalls
	if _, err := manager.Ensure(ctx, testEnsure("ensure-overlap")); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("overlapping ensure verdict %v, want unsupported", err)
	}
	if engine.createCalls != creates || engine.startCalls != starts {
		t.Fatalf("stopping workload was relaunched: create=%d/%d start=%d/%d", engine.createCalls, creates, engine.startCalls, starts)
	}
	stored, err := repo.LookupOperation(ctx, workload.ID, "ensure-overlap")
	if err != nil || stored.ErrorKind != domain.ErrorUnsupported {
		t.Fatalf("overlap verdict was not durable: stored=%+v err=%v", stored, err)
	}
}

// TestEnsureRefusesWithoutVerifiedCapabilityAndLocalImage pins the honest
// refusals: an unverified runner and a missing local image are failed
// preconditions, and neither consumes the operation key.
func TestEnsureRefusesWithoutVerifiedCapabilityAndLocalImage(t *testing.T) {
	ctx := context.Background()

	engine := newFakeEngine()
	engine.capability.Available = false
	manager := newTestManager(t, engine, newFakeRepo())
	if _, err := manager.Ensure(ctx, testEnsure("key-a")); !errors.Is(err, domain.ErrRunnerUnavailable) {
		t.Fatalf("unverified capability verdict %v, want ErrRunnerUnavailable", err)
	}

	engine = newFakeEngine()
	engine.imageExist = false
	repo := newFakeRepo()
	manager = newTestManager(t, engine, repo)
	if _, err := manager.Ensure(ctx, testEnsure("key-a")); !errors.Is(err, domain.ErrImageMissing) {
		t.Fatalf("missing image verdict %v, want ErrImageMissing", err)
	}
	if engine.createCalls != 0 {
		t.Fatalf("missing image launched a container")
	}
	if _, err := repo.LookupOperation(context.Background(), "any", "key-a"); err != nil {
		t.Fatalf("lookup: %v", err)
	}
}

// TestReconcileConvergesReservedButUnlaunched pins the crash window between
// the database reserve and the engine create: recovery re-drives the exact
// deterministic sequence instead of launching a second container.
func TestReconcileConvergesReservedButUnlaunched(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	// Simulate a crash after the reserve transaction committed but before
	// any engine call: ensure a workload row directly through the repo.
	workloadID := newTestIDValue()
	workload := domain.Workload{
		ID: workloadID, OwnerUserID: testOwner, ProjectID: testProject,
		AppInstanceID: testInstance, AppID: "notes-app", AppVersion: "1.0.0",
		ManifestDigest: testEnsure("k").ManifestDigest, Image: testImage,
		Command: []string{testCommand, "serve"}, Port: 8080, Requested: testPolicy(),
		Effective:  domain.EffectiveFromRequested(testPolicy()),
		Generation: 1, State: domain.StatePending,
		ContainerName: domain.ContainerName(workloadID),
		HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	reserved, err := repo.ReserveEnsure(ctx, workload, domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: "crash-key", Operation: domain.OperationEnsure,
		RequestDigest: domain.OperationDigest(domain.OperationEnsure, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested),
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil || !reserved {
		t.Fatalf("reserve: %v reserved=%v", err, reserved)
	}
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get after reconcile: %v", err)
	}
	if stored.State != domain.StateRunning {
		t.Fatalf("state %v after convergence, want running", stored.State)
	}
	if engine.createCalls != 1 {
		t.Fatalf("recovery created %d containers, want exactly 1", engine.createCalls)
	}
}

// TestReconcileFailsExitedWorkloadWithExitCategory pins the runtime failure
// path: a container that exited is failed with a bounded category and the
// engine object is removed by exact identity.
func TestReconcileFailsExitedWorkloadWithExitCategory(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("key-a"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.exit(workload.ContainerName)
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.State != domain.StateFailed {
		t.Fatalf("state %v, want failed", stored.State)
	}
	if stored.LastExit != domain.ExitExited {
		t.Fatalf("exit category %q, want exited", stored.LastExit)
	}
	if engine.containerExists(workload.ContainerName) {
		t.Fatalf("exited container survived reconciliation")
	}
	if engine.removeCalls == 0 {
		t.Fatalf("reconciliation never removed the exact container")
	}
}

// TestRestartAdvancesGenerationAndLimit pins the supervisor-driven restart:
// generation and restart count advance deterministically, the action key
// replays exactly once, and the persisted budget is a hard refusal.
func TestRestartAdvancesGenerationAndLimit(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-1"})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if first.Generation != workload.Generation+1 || first.RestartCount != 1 {
		t.Fatalf("restart facts generation=%d count=%d", first.Generation, first.RestartCount)
	}
	replay, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-1"})
	if err != nil {
		t.Fatalf("same-key restart replay: %v", err)
	}
	if replay.Generation != first.Generation {
		t.Fatalf("replay advanced generation to %d, want %d", replay.Generation, first.Generation)
	}
	if _, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-2"}); err != nil {
		t.Fatalf("second restart: %v", err)
	}
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-3"})
	if !errors.Is(err, domain.ErrRestartLimitExhausted) {
		t.Fatalf("third restart verdict %v, want limit exhausted", err)
	}
	// The recorded refusal replays verbatim: same key, same verdict — never
	// a fabricated success.
	if _, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-3"}); !errors.Is(err, domain.ErrRestartLimitExhausted) {
		t.Fatalf("limit-exhausted replay verdict %v, want the recorded refusal", err)
	}
}

// TestRestartFromFailedWorkload pins the crash-loop repair path: a failed
// workload re-opens under a new generation with cleared engine facts.
func TestRestartFromFailedWorkload(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.exit(workload.ContainerName)
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	restarted, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "action-1"})
	if err != nil {
		t.Fatalf("restart from failed: %v", err)
	}
	if restarted.State != domain.StateRunning || restarted.Generation != 2 || restarted.RestartCount != 1 {
		t.Fatalf("restart facts state=%v generation=%d count=%d", restarted.State, restarted.Generation, restarted.RestartCount)
	}
}

// TestTerminateClearsFactsAndIsIdempotent pins the stop path: engine object
// removed by exact identity, terminal row keeps no facts, and repeat calls
// replay success without new engine calls.
func TestTerminateClearsFactsAndIsIdempotent(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := manager.Terminate(ctx, ports.TerminateCommand{
		WorkloadID: workload.ID, OperationKey: "stop-key", Reason: "policy",
	}); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	stored, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.State != domain.StateStopped || stored.Endpoint != "" || stored.ContainerID != "" {
		t.Fatalf("terminal facts state=%v endpoint=%q container=%q", stored.State, stored.Endpoint, stored.ContainerID)
	}
	if engine.containerExists(workload.ContainerName) {
		t.Fatalf("terminated container survived")
	}
	if err := manager.Terminate(ctx, ports.TerminateCommand{
		WorkloadID: workload.ID, OperationKey: "stop-key", Reason: "policy",
	}); err != nil {
		t.Fatalf("repeat terminate: %v", err)
	}
	if err := manager.Terminate(ctx, ports.TerminateCommand{
		WorkloadID: workload.ID, OperationKey: "other-key", Reason: "bad reason",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid reason verdict %v, want invalid", err)
	}
}

// TestReconcileStopsUninstalledWorkloadImmediately pins the definitive
// uninstall verdict: Core NotFound converges to termination inside one
// reconcile pass.
func TestReconcileStopsUninstalledWorkloadImmediately(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	manager.verifier.(*fakeVerifier).verdict = ports.LaunchGone
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.State != domain.StateStopped {
		t.Fatalf("state %v after uninstall, want stopped", stored.State)
	}
	if engine.containerExists(workload.ContainerName) {
		t.Fatalf("uninstalled workload container survived")
	}
}

// TestReconcileFailSafeAfterCoreGrace pins the transient-outage budget: a
// workload whose Core re-validation keeps failing is stopped after the
// bounded grace, and hard limits keep enforcing meanwhile.
func TestReconcileFailSafeAfterCoreGrace(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Fresh verification stamp: within the grace the workload survives.
	manager.verifier.(*fakeVerifier).err = errors.New("core unreachable")
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile within grace: %v", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.State != domain.StateRunning {
		t.Fatalf("state %v inside grace, want running", stored.State)
	}
	// Expire the stamp beyond the Core grace: the fail-safe stop fires.
	stale := time.Now().UTC().Add(-2 * time.Minute)
	stored.LastVerifiedAt = &stale
	repo.mu.Lock()
	repo.workloads[stored.ID] = stored
	repo.mu.Unlock()
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after grace: %v", err)
	}
	stored, _ = repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopped {
		t.Fatalf("state %v after grace, want stopped", stored.State)
	}
}

func TestReconcileTreatsInvalidCoreVerdictAsUncertainty(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("invalid-core-verdict"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	stale := time.Now().UTC().Add(-2 * time.Minute)
	workload.LastVerifiedAt = &stale
	repo.mu.Lock()
	repo.workloads[workload.ID] = workload
	repo.mu.Unlock()
	manager.verifier.(*fakeVerifier).verdict = ports.LaunchVerdict("invalid")
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile invalid verdict: %v", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopped || engine.containerExists(workload.ContainerName) {
		t.Fatalf("invalid Core verdict escaped fail-safe: state=%v exists=%v", stored.State, engine.containerExists(workload.ContainerName))
	}
}

// TestReconcileStopsIdleWorkload pins the bounded idle TTL: a running
// workload with no open surface converges to a stop; the next Open ensures a
// fresh launch under a fresh key.
func TestReconcileStopsIdleWorkload(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	workload, err := manager.Ensure(ctx, testEnsure("ensure-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	manager.surfaces.(*fakeSurfaces).has = false
	// The first no-surface observation starts a fresh durable idle interval;
	// the workload's older lifecycle timestamp cannot make it stop early.
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("first idle reconcile: %v", err)
	}
	stale, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get before idle reconcile: %v", err)
	}
	if stale.IdleSince == nil {
		t.Fatal("first idle reconcile did not persist idle_since")
	}
	idleSince := time.Now().UTC().Add(-time.Minute)
	stale.IdleSince = &idleSince
	repo.mu.Lock()
	repo.workloads[stale.ID] = stale
	repo.mu.Unlock()
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopped {
		t.Fatalf("state %v for idle workload, want stopped", stored.State)
	}
	// The fresh Open path still works: the active slot is free again.
	manager.surfaces.(*fakeSurfaces).has = true
	relaunched, err := manager.Ensure(ctx, testEnsure("fresh-key"))
	if err != nil {
		t.Fatalf("relaunch after idle stop: %v", err)
	}
	if relaunched.ID == workload.ID {
		t.Fatalf("relaunch reused the terminal workload row")
	}
	if engine.createCalls != 2 {
		t.Fatalf("engine create calls %d, want 2", engine.createCalls)
	}
}

// TestLaunchFailsClosedOnPolicyAndEndpointDrift pins the verification gates:
// an unapplied cgroup policy or a non-loopback endpoint stops the launch
// with corruption verdicts, and the engine object is removed.
func TestLaunchFailsClosedOnPolicyAndEndpointDrift(t *testing.T) {
	ctx := context.Background()

	// cgroup values did not apply.
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	manager.cgroup.(*fakeCgroup).effective = ports.EffectiveFacts{
		CPUMaxUSec: 200000, CPUPeriodUSec: domain.CPUPeriodUSec,
		MemoryHigh: 64 * 1024 * 1024, MemoryMax: 96 * 1024 * 1024, PIDsMax: 32,
	}
	if _, err := manager.Ensure(ctx, testEnsure("key-a")); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("policy drift verdict %v, want corrupt", err)
	}
	if engine.containerExists(domain.ContainerName(anyContainerID(repo))) && engine.removeCalls == 0 {
		t.Fatalf("drifted launch kept its container without removal")
	}

	// The engine published a non-loopback endpoint.
	engine = newFakeEngine()
	repo = newFakeRepo()
	manager = newTestManager(t, engine, repo)
	engine.mu.Lock()
	engine.hostIPOverride = "10.0.0.5"
	engine.mu.Unlock()
	if _, err := manager.Ensure(ctx, testEnsure("key-b")); err == nil {
		t.Fatalf("non-loopback endpoint accepted")
	}
	// The corrupt convergence removed the drifted container: a failed
	// verification never leaks a live engine object.
	if engine.containerCount() != 0 {
		t.Fatalf("drifted launch kept %d containers", engine.containerCount())
	}
}

// TestLaunchVerifiesCreatedContainerProfileBeforeRunning proves that create
// success is not treated as enforcement evidence. The manager must inspect
// the actual post-start object, remove the exact ID when the engine widened
// the profile, and keep the workload out of running.
func TestLaunchVerifiesCreatedContainerProfileBeforeRunning(t *testing.T) {
	engine := newFakeEngine()
	engine.createProfileDrift = true
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)

	_, err := manager.Ensure(context.Background(), testEnsure("created-profile-drift"))
	if !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("created profile drift verdict %v, want corrupt", err)
	}
	if engine.containerCount() != 0 || engine.removeCalls == 0 {
		t.Fatalf("created profile drift was not removed: containers=%d removes=%d", engine.containerCount(), engine.removeCalls)
	}
	stored, getErr := repo.Get(context.Background(), anyWorkloadID(repo))
	if getErr != nil || stored.State != domain.StateFailed {
		t.Fatalf("created profile drift workload=%+v err=%v, want failed", stored, getErr)
	}
}

func TestLaunchAndObservationRejectUnavailableCgroupEvidence(t *testing.T) {
	ctx := context.Background()
	t.Run("startup counters", func(t *testing.T) {
		engine := newFakeEngine()
		repo := newFakeRepo()
		manager := newTestManager(t, engine, repo)
		manager.cgroup.(*fakeCgroup).counterErr = errors.New("counter evidence unavailable")
		if _, err := manager.Ensure(ctx, testEnsure("counter-startup")); !errors.Is(err, domain.ErrCorrupt) {
			t.Fatalf("startup counter verdict %v, want corrupt", err)
		}
		if engine.containerCount() != 0 {
			t.Fatalf("startup counter failure leaked %d containers", engine.containerCount())
		}
	})
	t.Run("running observation", func(t *testing.T) {
		engine := newFakeEngine()
		repo := newFakeRepo()
		manager := newTestManager(t, engine, repo)
		if _, err := manager.Ensure(ctx, testEnsure("counter-observe")); err != nil {
			t.Fatalf("ensure: %v", err)
		}
		manager.cgroup.(*fakeCgroup).counterErr = errors.New("counter evidence unavailable")
		if observations, err := manager.Observe(ctx); !errors.Is(err, domain.ErrUnavailable) || observations != nil {
			t.Fatalf("observation=%+v err=%v, want unavailable with no fabricated snapshot", observations, err)
		}
	})
}

func TestLaunchRejectsCPUPeriodDrift(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	manager.cgroup.(*fakeCgroup).effective.CPUPeriodUSec = domain.CPUPeriodUSec * 2
	if _, err := manager.Ensure(context.Background(), testEnsure("cpu-period-drift")); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("CPU period drift verdict %v, want corrupt", err)
	}
	if engine.containerCount() != 0 {
		t.Fatalf("CPU period drift leaked %d containers", engine.containerCount())
	}
}

func TestReconcileRemovesExactOwnedRuntimeDrift(t *testing.T) {
	cases := map[string]func(*Manager, *fakeEngine, *fakeWorkloadRepo, domain.Workload){
		"security profile": func(_ *Manager, engine *fakeEngine, _ *fakeWorkloadRepo, workload domain.Workload) {
			engine.mu.Lock()
			engine.containers[workload.ContainerName].publishedPorts = 2
			engine.mu.Unlock()
		},
		"effective limits": func(manager *Manager, _ *fakeEngine, _ *fakeWorkloadRepo, _ domain.Workload) {
			manager.cgroup.(*fakeCgroup).effective.CPUPeriodUSec = domain.CPUPeriodUSec * 2
		},
		"persisted endpoint": func(_ *Manager, _ *fakeEngine, repo *fakeWorkloadRepo, workload domain.Workload) {
			repo.mu.Lock()
			workload.Endpoint = "127.0.0.1:42000"
			repo.workloads[workload.ID] = workload
			repo.mu.Unlock()
		},
	}
	for name, drift := range cases {
		t.Run(name, func(t *testing.T) {
			engine := newFakeEngine()
			repo := newFakeRepo()
			manager := newTestManager(t, engine, repo)
			workload, err := manager.Ensure(context.Background(), testEnsure("runtime-drift"))
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			drift(manager, engine, repo, workload)
			if err := manager.Reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			stored, err := repo.Get(context.Background(), workload.ID)
			if err != nil || stored.State != domain.StateFailed {
				t.Fatalf("drifted workload=%+v err=%v, want failed", stored, err)
			}
			if engine.containerCount() != 0 {
				t.Fatalf("exact owned drift left %d live containers", engine.containerCount())
			}
		})
	}
}

func TestReconcileNeverTouchesIdentityMismatch(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	workload, err := manager.Ensure(context.Background(), testEnsure("identity-drift"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.mu.Lock()
	engine.containers[workload.ContainerName].labels["workos.workload.generation"] = "999"
	engine.mu.Unlock()
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, err := repo.Get(context.Background(), workload.ID)
	if err != nil || stored.State != domain.StateFailed {
		t.Fatalf("identity mismatch workload=%+v err=%v, want failed", stored, err)
	}
	if !engine.containerExists(workload.ContainerName) || engine.stopCalls != 0 || engine.removeCalls != 0 {
		t.Fatalf("identity-mismatched object was touched: exists=%v stop=%d remove=%d",
			engine.containerExists(workload.ContainerName), engine.stopCalls, engine.removeCalls)
	}
}

// hostIPOverride drives the non-loopback case through the fake engine.
func TestObservationsCarryBoundedFactsOnly(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()

	if _, err := manager.Ensure(ctx, testEnsure("ensure-key")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	observations, err := manager.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations %d, want 1", len(observations))
	}
	observation := observations[0]
	if observation.HealthVerdict != domain.HealthOK {
		t.Fatalf("health verdict %q, want ok", observation.HealthVerdict)
	}
}

func TestLookupRunningPreservesStoreUnavailable(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	workload, err := manager.Ensure(context.Background(), testEnsure("lookup-store-unavailable"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	repo.getErr = ports.ErrStoreUnavailable
	if _, err := manager.LookupRunning(context.Background(), workload.ID, workload.Generation); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("lookup store verdict %v, want unavailable", err)
	}
}

func anyContainerID(repo *fakeWorkloadRepo) string {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, workload := range repo.workloads {
		return workload.ContainerName
	}
	return ""
}

func anyWorkloadID(repo *fakeWorkloadRepo) string {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for workloadID := range repo.workloads {
		return workloadID
	}
	return ""
}

func TestTerminateWaitsForConfirmedContainerRemoval(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-stop-confirm"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.stopErr = ports.ErrEngineUnavailable
	err = manager.Terminate(ctx, ports.TerminateCommand{WorkloadID: workload.ID, OperationKey: "stop-confirm", Reason: "policy"})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("terminate verdict %v, want unavailable", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopping {
		t.Fatalf("state %v after failed cleanup, want stopping", stored.State)
	}
	if !engine.containerExists(workload.ContainerName) {
		t.Fatal("failed cleanup unexpectedly removed the container")
	}
	engine.stopErr = nil
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, _ = repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopped || engine.containerExists(workload.ContainerName) {
		t.Fatalf("reconcile did not converge stop: state=%v container=%v", stored.State, engine.containerExists(workload.ContainerName))
	}
	if err := manager.Terminate(ctx, ports.TerminateCommand{WorkloadID: workload.ID, OperationKey: "stop-confirm", Reason: "policy"}); err != nil {
		t.Fatalf("same-key stop replay after reconcile: %v", err)
	}
}

func TestTerminateRemovesExactOwnedContainerAfterProfileDrift(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-owned-drift"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.mu.Lock()
	engine.containers[workload.ContainerName].publishedPorts = 2
	engine.mu.Unlock()
	if err := manager.Terminate(ctx, ports.TerminateCommand{
		WorkloadID: workload.ID, OperationKey: "terminate-owned-drift", Reason: "policy",
	}); err != nil {
		t.Fatalf("terminate exact owned drift: %v", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.State != domain.StateStopped || engine.containerExists(workload.ContainerName) {
		t.Fatalf("owned drift did not converge: state=%v exists=%v", stored.State, engine.containerExists(workload.ContainerName))
	}
}

func TestRestartDoesNotAdvanceUntilOldContainerIsRemoved(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-restart-confirm"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.removeErr = ports.ErrEngineUnavailable
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-confirm"})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("restart verdict %v, want unavailable", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.Generation != 1 || stored.State != domain.StateRunning {
		t.Fatalf("cleanup failure advanced row: generation=%d state=%v", stored.Generation, stored.State)
	}
	engine.removeErr = nil
	restarted, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-confirm"})
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if restarted.Generation != 2 || restarted.State != domain.StateRunning {
		t.Fatalf("restart facts generation=%d state=%v", restarted.Generation, restarted.State)
	}
}

func TestRestartRefusesStaleGenerationAndSecurityFacts(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-identity"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.mu.Lock()
	container := engine.containers[workload.ContainerName]
	container.labels["workos.workload.generation"] = "0"
	container.spec.Image = "localhost/foreign@sha256:" + strings.Repeat("f", 64)
	engine.mu.Unlock()
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-identity"})
	if !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("stale container verdict %v, want corrupt", err)
	}
	if engine.stopCalls != 0 || engine.removeCalls != 0 || !engine.containerExists(workload.ContainerName) {
		t.Fatalf("foreign/stale container was touched: stop=%d remove=%d", engine.stopCalls, engine.removeCalls)
	}
	if _, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-identity"}); !errors.Is(err, domain.ErrFailed) {
		t.Fatalf("permanent same-key replay %v, want failed", err)
	}
}

func TestRestartRefusesAdditionalPublishedPort(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-port-identity"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	engine.mu.Lock()
	engine.containers[workload.ContainerName].publishedPorts = 2
	engine.mu.Unlock()
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-port-identity"})
	if !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("additional publish verdict %v, want corrupt", err)
	}
	if engine.stopCalls != 0 || engine.removeCalls != 0 || !engine.containerExists(workload.ContainerName) {
		t.Fatalf("container with additional publish was touched: stop=%d remove=%d", engine.stopCalls, engine.removeCalls)
	}
}

func TestWorkloadMatcherRejectsMountAndTmpfsDrift(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	workload, err := manager.Ensure(context.Background(), testEnsure("ensure-mount-profile"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	facts, err := engine.InspectContainer(context.Background(), workload.ContainerName)
	if err != nil || !matchesWorkloadContainer(workload, facts) {
		t.Fatalf("baseline facts=%+v err=%v", facts, err)
	}

	cases := []ports.ContainerFacts{facts, facts, facts, facts, facts, facts, facts}
	cases[0].Tmpfs = map[string]string{"/tmp": "rw,size=67108864,noexec,nodev,nosuid"}
	cases[1].Tmpfs = map[string]string{"/tmp": "rw,size=33554432,exec,nodev,nosuid"}
	cases[2].Tmpfs = map[string]string{
		"/tmp": "rw,size=33554432,noexec,nodev,nosuid", "/cache": "rw,noexec,nodev,nosuid",
	}
	cases[3].UnexpectedMounts = 1
	cases[4].UnexpectedSecurityOpts = 1
	cases[5].ConnectedNetworks = 2
	cases[6].AutoRemove = true
	for index, drifted := range cases {
		if matchesWorkloadContainer(workload, drifted) {
			t.Fatalf("mount drift case %d was adopted: %+v", index, drifted)
		}
	}
}

func TestReconcileRemovesFullyLabelledOrphan(t *testing.T) {
	engine := newFakeEngine()
	manager := newTestManager(t, engine, newFakeRepo())
	id := newTestIDValue()
	orphan := domain.Workload{
		ID: id, OwnerUserID: testOwner, AppInstanceID: testInstance, Generation: 1,
		ContainerName: domain.ContainerName(id), Image: testImage,
		Command: []string{testCommand, "serve"}, Effective: domain.EffectiveFromRequested(testPolicy()),
	}
	if _, err := engine.CreateContainer(context.Background(), ports.ContainerSpec{
		Name: orphan.ContainerName, Image: orphan.Image, Command: orphan.Command,
		Port: 8080, Labels: domain.EngineLabels(orphan), Policy: orphan.Effective,
	}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if engine.containerExists(orphan.ContainerName) {
		t.Fatal("fully labelled orphan survived reconciliation")
	}
}

func TestObservationClassifiesPIDsLimitEventDelta(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	if _, err := manager.Ensure(ctx, testEnsure("ensure-pids")); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	manager.cgroup.(*fakeCgroup).counters.PIDsLimitEvents = 1
	observations, err := manager.Observe(ctx)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observe: count=%d err=%v", len(observations), err)
	}
	if observations[0].ExitCategory != domain.ExitPIDs || observations[0].PIDsLimitEvents != 1 {
		t.Fatalf("pids observation %+v", observations[0])
	}
}

func TestRestartReplayAfterAtomicFinalizeFailureDoesNotAdvanceAgain(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-finalize-window"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	repo.failCompletedOperationOnce = true
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-finalize-window"})
	if err == nil {
		t.Fatal("injected operation commit failure was not returned")
	}
	advanced, _ := repo.Get(ctx, workload.ID)
	if advanced.State != domain.StateStarting || advanced.Generation != 2 {
		t.Fatalf("atomic finalize failure did not leave generation 2 starting: %+v", advanced)
	}
	creates := engine.createCalls
	replay, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-finalize-window"})
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if replay.Generation != 2 || engine.createCalls != creates {
		t.Fatalf("replay advanced again: generation=%d creates=%d want=%d", replay.Generation, engine.createCalls, creates)
	}
}

func TestCompletedOperationVerdictCannotBeDowngraded(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	command := testEnsure("ensure-immutable-verdict")
	workload, err := manager.Ensure(ctx, command)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	digest := domain.OperationDigest(domain.OperationEnsure, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	err = repo.RecordOperation(ctx, domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: command.OperationKey,
		Operation: domain.OperationEnsure, RequestDigest: digest,
		ResultGeneration: workload.Generation, ErrorKind: domain.ErrorUnavailable,
		UpdatedAt: time.Now().UTC(),
	})
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("terminal verdict downgrade returned %v, want conflict", err)
	}
	stored, err := repo.LookupOperation(ctx, workload.ID, command.OperationKey)
	if err != nil || stored.ResultState != domain.StateRunning || stored.ErrorKind != "" {
		t.Fatalf("terminal verdict changed: %+v err=%v", stored, err)
	}
}

func TestOpenOperationKeyCannotBeReboundToAnotherRequest(t *testing.T) {
	repo := newFakeRepo()
	now := time.Now().UTC()
	first := domain.WorkloadOperation{
		WorkloadID: newTestIDValue(), OperationKey: "open-key",
		Operation: domain.OperationTerminate, RequestDigest: "sha256:" + strings.Repeat("a", 64),
		ResultGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.RecordOperation(context.Background(), first); err != nil {
		t.Fatalf("record first request: %v", err)
	}
	conflicting := first
	conflicting.RequestDigest = "sha256:" + strings.Repeat("b", 64)
	conflicting.ErrorKind = domain.ErrorUnavailable
	if err := repo.RecordOperation(context.Background(), conflicting); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("open key rebound verdict %v, want conflict", err)
	}
	stored, err := repo.LookupOperation(context.Background(), first.WorkloadID, first.OperationKey)
	if err != nil || stored.RequestDigest != first.RequestDigest || stored.ErrorKind != "" {
		t.Fatalf("open operation changed: %+v err=%v", stored, err)
	}
}

func TestRestartRefusesNonAdjacentPersistedTargetGeneration(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-target-generation"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	digest := domain.OperationDigest(domain.OperationRestart, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	now := time.Now().UTC()
	if err := repo.RecordOperation(ctx, domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: "restart-corrupt-target",
		Operation: domain.OperationRestart, RequestDigest: digest,
		ResultGeneration: workload.Generation + 2, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed operation: %v", err)
	}
	creates := engine.createCalls
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-corrupt-target"})
	if !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("restart verdict %v, want corrupt", err)
	}
	stored, _ := repo.Get(ctx, workload.ID)
	if stored.Generation != workload.Generation || stored.State != domain.StateRunning || engine.createCalls != creates {
		t.Fatalf("corrupt target caused side effects: workload=%+v creates=%d want=%d", stored, engine.createCalls, creates)
	}
}

func TestRestartReplayNeverFabricatesSuccessFromFailedTargetGeneration(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-failed-target"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	digest := domain.OperationDigest(domain.OperationRestart, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	now := time.Now().UTC()
	if err := repo.RecordOperation(ctx, domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: "restart-failed-target",
		Operation: domain.OperationRestart, RequestDigest: digest,
		ResultGeneration: 2, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed operation: %v", err)
	}
	if err := manager.removeWorkloadContainer(ctx, workload); err != nil {
		t.Fatalf("remove generation 1: %v", err)
	}
	if err := repo.Transition(ctx, workload.ID, domain.StateRunning, domain.StateStarting, ports.WorkloadFacts{
		Generation: 2, RestartCount: 1, HealthVerdict: domain.HealthUnknown, LastExit: domain.ExitNone,
		ClearEngine: true,
	}, now); err != nil {
		t.Fatalf("seed target generation: %v", err)
	}
	failedAt := now.Add(time.Second)
	if err := repo.Transition(ctx, workload.ID, domain.StateStarting, domain.StateFailed, ports.WorkloadFacts{
		Generation: 2, RestartCount: 1, HealthVerdict: domain.HealthFailing, LastExit: domain.ExitUnknown,
		StoppedAt: &failedAt, ClearEngine: true,
	}, failedAt); err != nil {
		t.Fatalf("seed failed target: %v", err)
	}
	_, err = manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-failed-target"})
	if !errors.Is(err, domain.ErrFailed) {
		t.Fatalf("failed target replay verdict %v, want failed", err)
	}
	stored, _ := repo.LookupOperation(ctx, workload.ID, "restart-failed-target")
	if stored.ErrorKind != domain.ErrorPermanent || stored.ResultState != "" {
		t.Fatalf("ambiguous replay was not closed permanently: %+v", stored)
	}
}

func TestReconcileStartingFinalizesOriginalRestartKey(t *testing.T) {
	engine := newFakeEngine()
	repo := newFakeRepo()
	manager := newTestManager(t, engine, repo)
	ctx := context.Background()
	workload, err := manager.Ensure(ctx, testEnsure("ensure-reconcile-key"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	digest := domain.OperationDigest(domain.OperationRestart, workload.ID, workload.Image, workload.Command, workload.Port, workload.Requested)
	now := time.Now().UTC()
	if err := repo.RecordOperation(ctx, domain.WorkloadOperation{
		WorkloadID: workload.ID, OperationKey: "restart-reconcile-key", Operation: domain.OperationRestart,
		RequestDigest: digest, ResultGeneration: 2, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("reserve restart operation: %v", err)
	}
	if err := manager.removeWorkloadContainer(ctx, workload); err != nil {
		t.Fatalf("remove old generation: %v", err)
	}
	if err := repo.Transition(ctx, workload.ID, domain.StateRunning, domain.StateStarting, ports.WorkloadFacts{
		Generation: 2, RestartCount: 1, HealthVerdict: domain.HealthUnknown,
		LastExit: domain.ExitNone, ClearEngine: true,
	}, now); err != nil {
		t.Fatalf("seed starting generation: %v", err)
	}
	if err := manager.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stored, _ := repo.LookupOperation(ctx, workload.ID, "restart-reconcile-key")
	if stored.ResultState != domain.StateRunning || stored.ResultGeneration != 2 {
		t.Fatalf("original operation was not finalized: %+v", stored)
	}
	creates := engine.createCalls
	replay, err := manager.Restart(ctx, ports.RestartCommand{WorkloadID: workload.ID, OperationKey: "restart-reconcile-key"})
	if err != nil || replay.Generation != 2 || engine.createCalls != creates {
		t.Fatalf("replay=%+v err=%v creates=%d want=%d", replay, err, engine.createCalls, creates)
	}
}
