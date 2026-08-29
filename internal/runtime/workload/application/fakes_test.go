package application

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// The fakes below are deterministic: the engine records every argv-relevant
// call so idempotency and crash-window convergence are counted, never
// assumed.

const (
	testOwner     = "0198d7ea-2110-7c42-b659-c5e4d73bc341"
	testProject   = "0198d7ea-2110-7c42-b659-c5e4d73bc342"
	testInstance  = "0198d7ea-2110-7c42-b659-c5e4d73bc343"
	testImage     = "localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testCommand   = "/workos-fixture"
	testOperation = "ensure-key-1"
)

func testPolicy() domain.RequestedPolicy {
	return domain.RequestedPolicy{
		CPUHardCores: 1, MemoryHighMB: 64, MemoryMaxMB: 96, PidsMax: 32,
		HTTPPath: "/health", StartupSeconds: 10, RestartLimit: 2,
	}
}

func testEnsure(key string) ports.EnsureCommand {
	return ports.EnsureCommand{
		OwnerUserID: testOwner, ProjectID: testProject, AppInstanceID: testInstance,
		AppID: "notes-app", AppVersion: "1.0.0", ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		Image: testImage, Command: []string{testCommand, "serve"}, Port: 8080,
		Requested: testPolicy(), OperationKey: key,
	}
}

func newTestConfig() Config {
	return Config{
		ReconcileInterval: 15 * time.Second, IdleTTL: 30 * time.Second,
		OperationTimeout: 10 * time.Second, CoreGrace: time.Minute,
		LeaseTTL: 5 * time.Second, InstanceName: "runtime-test",
	}
}

func newTestManager(t *testing.T, engine *fakeEngine, repo *fakeWorkloadRepo) *Manager {
	t.Helper()
	prober := &fakeProber{verdict: domain.HealthOK}
	verifier := &fakeVerifier{verdict: ports.LaunchInstalled}
	surfaces := &fakeSurfaces{has: true}
	reader := &fakeCgroup{effective: ports.EffectiveFacts{
		CPUMaxUSec: 100000, MemoryHigh: 64 * 1024 * 1024, MemoryMax: 96 * 1024 * 1024, PIDsMax: 32,
	}}
	manager, err := New(repo, engine, reader, prober, verifier, surfaces, ids.UUIDv7{}, newTestConfig(), testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeEngine records call counts per operation so tests assert exactly-once
// engine side effects.
type fakeEngine struct {
	mu sync.Mutex

	capability ports.Capability
	imageExist bool

	createCalls int
	startCalls  int
	stopCalls   int
	removeCalls int

	containers map[string]*fakeContainer
	createErr  error
	startErr   error
	inspectErr error

	hostIPOverride string

	nextID int
}

type fakeContainer struct {
	id       string
	name     string
	running  bool
	exit     int
	oom      bool
	pid      int
	labels   map[string]string
	hostIP   string
	hostPort int32
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		capability: ports.Capability{Available: true, Rootless: true, CgroupV2: true, CgroupRoot: "/user.slice/user-1000.slice"},
		containers: make(map[string]*fakeContainer),
		imageExist: true,
	}
}

func (e *fakeEngine) Probe(context.Context) (ports.Capability, error) {
	return e.capability, nil
}

func (e *fakeEngine) ImageExists(_ context.Context, image string) (bool, error) {
	if !domain.ValidImage(image) {
		return false, domain.ErrInvalid
	}
	return e.imageExist, nil
}

func (e *fakeEngine) CreateContainer(_ context.Context, spec ports.ContainerSpec) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.createCalls++
	if e.createErr != nil {
		return "", e.createErr
	}
	if _, exists := e.containers[spec.Name]; exists {
		return "", ports.ErrContainerAlreadyExists
	}
	e.nextID++
	container := &fakeContainer{
		id: fmt.Sprintf("ctr-%d", e.nextID), name: spec.Name, running: false,
		pid: 42000 + e.nextID, labels: spec.Labels, hostIP: "127.0.0.1", hostPort: 41000 + int32(e.nextID),
	}
	e.containers[spec.Name] = container
	return container.id, nil
}

func (e *fakeEngine) StartContainer(_ context.Context, nameOrID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startCalls++
	if e.startErr != nil {
		return e.startErr
	}
	for _, container := range e.containers {
		if container.name == nameOrID || container.id == nameOrID {
			container.running = true
			return nil
		}
	}
	return ports.ErrContainerNotFound
}

func (e *fakeEngine) StopContainer(_ context.Context, nameOrID string, _ time.Duration) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopCalls++
	for _, container := range e.containers {
		if container.name == nameOrID || container.id == nameOrID {
			container.running = false
			return nil
		}
	}
	return ports.ErrContainerNotFound
}

func (e *fakeEngine) RemoveContainer(_ context.Context, nameOrID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removeCalls++
	for name, container := range e.containers {
		if container.name == nameOrID || container.id == nameOrID {
			delete(e.containers, name)
			return nil
		}
	}
	return ports.ErrContainerNotFound
}

func (e *fakeEngine) InspectContainer(_ context.Context, nameOrID string) (ports.ContainerFacts, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inspectErr != nil {
		return ports.ContainerFacts{}, e.inspectErr
	}
	for _, container := range e.containers {
		if container.name == nameOrID || container.id == nameOrID {
			hostIP := container.hostIP
			if e.hostIPOverride != "" {
				hostIP = e.hostIPOverride
			}
			return ports.ContainerFacts{
				ID: container.id, Name: container.name, Running: container.running,
				ExitCode: container.exit, PID: container.pid, OOMKilled: container.oom,
				Labels: container.labels, HostIP: hostIP, HostPort: container.hostPort,
			}, nil
		}
	}
	return ports.ContainerFacts{}, ports.ErrContainerNotFound
}

func (e *fakeEngine) ListManagedContainers(context.Context) ([]ports.ContainerFacts, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	facts := make([]ports.ContainerFacts, 0, len(e.containers))
	for _, container := range e.containers {
		facts = append(facts, ports.ContainerFacts{ID: container.id, Name: container.name, Running: container.running, Labels: container.labels})
	}
	return facts, nil
}

func (e *fakeEngine) exit(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if container, ok := e.containers[name]; ok {
		container.running = false
		container.exit = 1
	}
}

func (e *fakeEngine) containerExists(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.containers[name]
	return ok
}

type fakeCgroup struct {
	effective ports.EffectiveFacts
	counters  ports.CgroupCounters
	paths     map[int]string
}

func (r *fakeCgroup) SelfSubtree() (string, error) { return "/user.slice/user-1000.slice", nil }

func (r *fakeCgroup) CgroupPathForPID(pid int) (string, error) {
	if path, ok := r.paths[pid]; ok {
		return path, nil
	}
	return "/user.slice/user-1000.slice/workos-wl-test", nil
}

func (r *fakeCgroup) ReadEffective(context.Context, string) (ports.EffectiveFacts, error) {
	return r.effective, nil
}

func (r *fakeCgroup) ReadCounters(context.Context, string) (ports.CgroupCounters, error) {
	return r.counters, nil
}

type fakeProber struct {
	verdict string
}

func (p *fakeProber) Probe(context.Context, string, string, time.Duration) (ports.HealthResult, error) {
	return ports.HealthResult{Verdict: p.verdict}, nil
}

type fakeVerifier struct {
	verdict ports.LaunchVerdict
	err     error
}

func (v *fakeVerifier) VerifyLaunch(context.Context, ports.LaunchQuery) (ports.LaunchVerdict, error) {
	if v.err != nil {
		return ports.LaunchUnknown, v.err
	}
	return v.verdict, nil
}

type fakeSurfaces struct {
	has bool
	err error
}

func (s *fakeSurfaces) HasActiveSurface(context.Context, string, string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.has, nil
}

// fakeWorkloadRepo is the in-memory repository; it mirrors the partial
// active-slot uniqueness so concurrency and recovery paths stay honest.
type fakeWorkloadRepo struct {
	mu         sync.Mutex
	workloads  map[string]domain.Workload
	operations map[string]domain.WorkloadOperation
}

func newFakeRepo() *fakeWorkloadRepo {
	return &fakeWorkloadRepo{workloads: map[string]domain.Workload{}, operations: map[string]domain.WorkloadOperation{}}
}

func opKey(workloadID, key string) string { return workloadID + "|" + key }

func (r *fakeWorkloadRepo) Get(_ context.Context, workloadID string) (domain.Workload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	workload, ok := r.workloads[workloadID]
	if !ok {
		return domain.Workload{}, domain.ErrNotFound
	}
	return workload, nil
}

func (r *fakeWorkloadRepo) GetActiveByInstance(_ context.Context, ownerUserID, appInstanceID string) (domain.Workload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, workload := range r.workloads {
		if workload.OwnerUserID == ownerUserID && workload.AppInstanceID == appInstanceID && workload.Active() {
			return workload, nil
		}
	}
	return domain.Workload{}, domain.ErrNotFound
}

func (r *fakeWorkloadRepo) List(_ context.Context, limit int) ([]domain.Workload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Workload, 0, len(r.workloads))
	for _, workload := range r.workloads {
		result = append(result, workload)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (r *fakeWorkloadRepo) ReserveEnsure(_ context.Context, workload domain.Workload, op domain.WorkloadOperation) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.workloads {
		if existing.OwnerUserID == workload.OwnerUserID && existing.AppInstanceID == workload.AppInstanceID && existing.Active() {
			return false, nil
		}
	}
	if _, exists := r.operations[opKey(op.WorkloadID, op.OperationKey)]; exists {
		return false, nil
	}
	r.workloads[workload.ID] = workload
	r.operations[opKey(op.WorkloadID, op.OperationKey)] = op
	return true, nil
}

func (r *fakeWorkloadRepo) LookupOperation(_ context.Context, workloadID, key string) (ports.StoredOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.operations[opKey(workloadID, key)]
	if !ok {
		return ports.StoredOperation{}, nil
	}
	return ports.StoredOperation{
		WorkloadID: stored.WorkloadID, OperationKey: stored.OperationKey,
		Operation: stored.Operation, RequestDigest: stored.RequestDigest,
		ResultState: stored.ResultState, ResultGeneration: stored.ResultGeneration,
		ErrorKind: stored.ErrorKind,
	}, nil
}

func (r *fakeWorkloadRepo) RecordOperation(_ context.Context, op domain.WorkloadOperation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.operations[opKey(op.WorkloadID, op.OperationKey)]
	if ok {
		if existing.CreatedAt.IsZero() {
			op.CreatedAt = existing.CreatedAt
		} else {
			op.CreatedAt = existing.CreatedAt
		}
	}
	r.operations[opKey(op.WorkloadID, op.OperationKey)] = op
	return nil
}

func (r *fakeWorkloadRepo) Transition(_ context.Context, workloadID string, from, to domain.State, facts ports.WorkloadFacts, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	workload, ok := r.workloads[workloadID]
	if !ok || workload.State != from {
		return domain.ErrNotFound
	}
	// Guard exactly like the real SQL: the restart transition may re-open a
	// failed workload, every other transition refuses out of terminal rows.
	if to == domain.StateStarting {
		if from != domain.StateRunning && from != domain.StateFailed {
			return domain.ErrNotFound
		}
	} else if from.Terminal() {
		return domain.ErrNotFound
	}
	workload.State = to
	workload.Generation = facts.Generation
	workload.RestartCount = facts.RestartCount
	workload.HealthVerdict = facts.HealthVerdict
	workload.LastExit = facts.LastExit
	workload.UpdatedAt = now
	if facts.ClearEngine {
		workload.ContainerID, workload.Endpoint, workload.CgroupPath = "", "", ""
		workload.StoppedAt = facts.StoppedAt
	} else {
		workload.ContainerID = facts.ContainerID
		workload.Endpoint = facts.Endpoint
		workload.CgroupPath = facts.CgroupPath
		workload.StartedAt = facts.StartedAt
	}
	if facts.VerifiedAt != nil {
		workload.LastVerifiedAt = facts.VerifiedAt
	}
	r.workloads[workloadID] = workload
	return nil
}

func (r *fakeWorkloadRepo) ClaimLease(context.Context, string, string, time.Time) (bool, error) {
	return true, nil
}
