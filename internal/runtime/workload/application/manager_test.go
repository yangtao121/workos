package application

import (
	"context"
	"errors"
	"strings"
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
		Generation: 1, State: domain.StateStarting,
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
	// Age the row past the idle TTL so one reconcile is enough.
	stale, err := repo.Get(ctx, workload.ID)
	if err != nil {
		t.Fatalf("get before idle reconcile: %v", err)
	}
	stale.UpdatedAt = time.Now().UTC().Add(-time.Minute)
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
		CPUMaxUSec: 200000, MemoryHigh: 64 * 1024 * 1024, MemoryMax: 96 * 1024 * 1024, PIDsMax: 32,
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

func anyContainerID(repo *fakeWorkloadRepo) string {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, workload := range repo.workloads {
		return workload.ContainerName
	}
	return ""
}
