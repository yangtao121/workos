// Package fakefixture implements the ports.Engine contract with a bounded,
// in-process, deterministic container simulator (ADR-0016 §2). It exists so
// the supervision software chain — observation, incident decisions, restart/
// stop actions, action ledger, notifications — can be exercised
// cross-process on hosts without rootless Podman. It provides NO container
// isolation, NO cgroup enforcement, and NO network namespace: it proves the
// software chain only, never the container capability.
//
// Failure scripting: when the operator/test configures a scenario file path
// (Config.ScenarioFile), the engine reads it before every inspection and
// deterministic health probe. The bounded grammar selects, per container
// name, one of: `ok` (default), `crash` (exit after N inspections), `oom`
// (OOM-killed exit), `flap` (healthy/failing alternation). Missing file or
// unknown grammar means `ok`; malformed content never crashes the engine.
package fakefixture

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	workloadports "github.com/yangtao121/workos/internal/runtime/workload/ports"
)

type scenario int

const (
	scenarioOK scenario = iota
	scenarioCrash
	scenarioOOM
	scenarioFlap
)

type container struct {
	id        string
	name      string
	image     string
	command   []string
	port      int64
	labels    map[string]string
	running   bool
	exitCode  int
	oomKilled bool
	pid       int
	inspected int
	startedAt time.Time
	policy    workloadports.EffectiveFacts
	hostPort  int32
	server    *http.Server
	listener  net.Listener
}

// State is the fixture engine's shared fact store: the engine records the
// effective policy per fixture PID and the cgroup reader reports exactly
// those values, mirroring a kernel that has really applied the limits
// (ADR-0016). Engine and reader share one State.
type State struct {
	mu       sync.Mutex
	policies map[int]workloadports.EffectiveFacts
}

func newState() *State { return &State{policies: map[int]workloadports.EffectiveFacts{}} }

// recordPolicy ties one fixture PID to the effective policy the engine was
// asked to enforce.
func (s *State) recordPolicy(pid int, facts workloadports.EffectiveFacts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[pid] = facts
}

// resolvePolicy returns the recorded policy of one fixture PID.
func (s *State) resolvePolicy(pid int) (workloadports.EffectiveFacts, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	facts, ok := s.policies[pid]
	return facts, ok
}

// Engine is the bounded fixture engine. All methods are safe for concurrent
// use; state lives only in this process.
type Engine struct {
	mu           sync.Mutex
	nextID       int
	nextPID      int
	containers   map[string]*container // by name and by id
	scenarioFile string
	now          func() time.Time
	state        *State
}

// New constructs the fixture engine with its paired cgroup reader.
// scenarioFile may be empty (every container runs the `ok` scenario).
func New(scenarioFile string) (*Engine, *CgroupReader) {
	state := newState()
	return &Engine{
		containers:   map[string]*container{},
		scenarioFile: scenarioFile,
		now:          time.Now,
		state:        state,
	}, &CgroupReader{state: state}
}

// Probe reports the engine as operational for the supervision software
// chain while Rootless stays false: the fixture runs simulated containers,
// never real isolated ones, and never claims the rootless container
// capability (ADR-0016 §2). The separate "rootless-container-runner"
// SystemService capability in runtime-host keeps the honest false verdict.
func (e *Engine) Probe(ctx context.Context) (workloadports.Capability, error) {
	return workloadports.Capability{
		Available:     true,
		Rootless:      false,
		CgroupV2:      true,
		Reason:        "fixture engine: supervision software-chain acceptance only; no real container isolation (ADR-0016)",
		EngineVersion: "fake-fixture/1.0.0",
		CgroupRoot:    "/workos/fixtures",
	}, nil
}

func (e *Engine) ImageExists(ctx context.Context, image string) (bool, error) {
	// The fixture accepts every digest-pinned reference: image presence is
	// not part of the software chain under test.
	return true, nil
}

func (e *Engine) CreateContainer(ctx context.Context, spec workloadports.ContainerSpec) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.containers[spec.Name]; exists {
		return "", workloadports.ErrContainerAlreadyExists
	}
	e.nextID++
	id := fmt.Sprintf("fake-%08x", e.nextID)
	c := &container{
		id: id, name: spec.Name, image: spec.Image, command: append([]string(nil), spec.Command...),
		port: spec.Port, labels: copyLabels(spec.Labels), pid: 0,
		hostPort:   int32(41000 + (e.nextID % 1000)),
		policy:     effectiveFromSpec(spec),
	}
	e.containers[spec.Name] = c
	e.containers[id] = c
	return id, nil
}

func (e *Engine) StartContainer(ctx context.Context, nameOrID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[nameOrID]
	if !ok {
		return workloadports.ErrContainerNotFound
	}
	if c.running {
		return fmt.Errorf("fixture engine: container %q is already running", nameOrID)
	}
	e.nextPID++
	c.running = true
	c.pid = e.nextPID
	c.exitCode = 0
	c.oomKilled = false
	c.inspected = 0
	c.startedAt = e.now()
	// A real loopback HTTP listener backs the fake published port so the
	// bounded startup/ongoing health probes run as real TCP round trips.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.running = false
		c.pid = 0
		return fmt.Errorf("fixture engine: health listener is unavailable: %w", err)
	}
	c.hostPort = int32(listener.Addr().(*net.TCPAddr).Port)
	// Record the exact effective policy under the fixture PID so the paired
	// cgroup reader's verification read matches what was requested.
	e.state.recordPolicy(e.nextPID, c.policy)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	c.server = server
	c.listener = listener
	go func(l net.Listener, srv *http.Server) {
		_ = srv.Serve(l)
	}(listener, server)
	return nil
}

func (e *Engine) StopContainer(ctx context.Context, nameOrID string, timeout time.Duration) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[nameOrID]
	if !ok {
		return fmt.Errorf("fixture engine: container %q is not known", nameOrID)
	}
	e.shutdownLocked(c)
	c.running = false
	c.pid = 0
	c.exitCode = 0
	return nil
}

// shutdownLocked stops the per-container health listener, if any.
func (e *Engine) shutdownLocked(c *container) {
	if c.server != nil {
		_ = c.server.Close()
		c.server = nil
		c.listener = nil
	}
}

func (e *Engine) RemoveContainer(ctx context.Context, nameOrID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[nameOrID]
	if !ok {
		// Removing an absent container is idempotent success, matching the
		// engine contract's cleanup convergence.
		return nil
	}
	delete(e.containers, c.name)
	delete(e.containers, c.id)
	return nil
}

func (e *Engine) InspectContainer(ctx context.Context, nameOrID string) (workloadports.ContainerFacts, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.containers[nameOrID]
	if !ok {
		return workloadports.ContainerFacts{}, workloadports.ErrContainerNotFound
	}
	c.inspected++
	switch e.scenarioFor(c.name) {
	case scenarioCrash:
		// Deterministic crash after the first inspection window: the real
		// container would exit between polls; the fixture models the exit
		// by tearing down the health listener too.
		if c.running && c.inspected >= 2 && e.now().Sub(c.startedAt) > 50*time.Millisecond {
			e.shutdownLocked(c)
			c.running = false
			c.exitCode = 1
			c.pid = 0
		}
	case scenarioOOM:
		if c.running && c.inspected >= 2 && e.now().Sub(c.startedAt) > 50*time.Millisecond {
			e.shutdownLocked(c)
			c.running = false
			c.exitCode = 137
			c.oomKilled = true
			c.pid = 0
		}
	case scenarioFlap:
		if c.running {
			c.exitCode = 0
		}
	default:
	}
	return workloadports.ContainerFacts{
		ID: c.id, Name: c.name, Running: c.running, ExitCode: c.exitCode, PID: c.pid,
		Labels: copyLabels(c.labels), HostIP: "127.0.0.1",
		HostPort: func() int32 {
			if c.running {
				return c.hostPort
			}
			return 0
		}(),
		ContainerPort: int32(c.port), PublishedPorts: 1, OOMKilled: c.oomKilled,
		Image: c.image, Command: c.command, ReadOnly: true, Privileged: false,
		CapabilitiesAdded: 0, EffectiveCapabilities: 0, BoundingCapabilities: 0,
		NoNewPrivileges: true, UnexpectedSecurityOpts: 0, AutoRemove: false,
		NetworkMode: "workos-app-internal", ConnectedNetworks: 1, InternalNetwork: true,
		RestartPolicy: "no", BindMounts: 0, UnexpectedMounts: 0, Devices: 0,
		Tmpfs: map[string]string{"/tmp": "rw,size=33554432,noexec,nodev,nosuid"},
	}, nil
}

func (e *Engine) ListManagedContainers(ctx context.Context) ([]workloadports.ContainerFacts, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]bool{}
	facts := make([]workloadports.ContainerFacts, 0, len(e.containers)/2)
	for _, c := range e.containers {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		facts = append(facts, workloadports.ContainerFacts{
			ID: c.id, Name: c.name, Running: c.running, ExitCode: c.exitCode, PID: c.pid,
			Labels: copyLabels(c.labels), Image: c.image, Command: c.command,
			ReadOnly: true, Privileged: false, CapabilitiesAdded: 0,
			Tmpfs: map[string]string{"/tmp": "rw,size=33554432,noexec,nodev,nosuid"},
		})
	}
	return facts, nil
}

// effectiveFromSpec mirrors the clamped effective policy the manager
// requested; the fixture "kernel" applies it verbatim.
func effectiveFromSpec(spec workloadports.ContainerSpec) workloadports.EffectiveFacts {
	return workloadports.EffectiveFacts{
		CPUMaxUSec:    spec.Policy.CPUQuotaUSec,
		CPUPeriodUSec: 100_000,
		MemoryHigh:    spec.Policy.MemoryHighBytes,
		MemoryMax:     spec.Policy.MemoryMaxBytes,
		PIDsMax:       spec.Policy.PidsMax,
	}
}

// scenarioFor reads the bounded scenario file; unknown names default to ok.
// The special name `*` matches every container, so cross-process supervision
// tests can flip behavior between polls without restarting runtime-host and
// without knowing the engine-derived container names.
func (e *Engine) scenarioFor(name string) scenario {
	if e.scenarioFile == "" {
		return scenarioOK
	}
	file, err := os.Open(e.scenarioFile)
	if err != nil {
		return scenarioOK
	}
	defer file.Close() //nolint:errcheck -- read-only handle
	exact := map[string]string{}
	wildcard := "ok"
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == "*" {
			wildcard = strings.TrimSpace(parts[1])
			continue
		}
		exact[parts[0]] = strings.TrimSpace(parts[1])
	}
	mode := wildcard
	if scoped, ok := exact[name]; ok {
		mode = scoped
	}
	switch mode {
	case "crash":
		return scenarioCrash
	case "oom":
		return scenarioOOM
	case "flap":
		return scenarioFlap
	default:
		return scenarioOK
	}
}

func copyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		out[key] = value
	}
	return out
}
