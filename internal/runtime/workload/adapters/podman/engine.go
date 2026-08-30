// Package podman adapts the neutral engine port to rootless Podman. Every
// invocation is a direct argv exec of the resolved absolute executable with a
// deadline and bounded output — never a shell — and no call ever pulls,
// logs in, or resolves a mutable tag. The adapter refuses to run containers
// unless the probe verified rootless mode, cgroup v2, and the required
// controllers (ADR-0006 §4).
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// Exec bounds for every engine invocation.
const (
	probeTimeout    = 15 * time.Second
	fastTimeout     = 15 * time.Second
	startTimeout    = 60 * time.Second
	stopGracePeriod = 10 * time.Second
	maxOutputBytes  = 1 << 20 // 1 MiB bounded output for every call
	// networkName is the WorkOS-owned internal network: created with
	// --internal so containers on it have no external egress route.
	networkName = "workos-app-internal"
)

// managedLabel is the exact label that marks a container as WorkOS-owned.
const managedLabel = "workos.managed"

// Engine is the Podman adapter. The binary path is resolved once at New.
type Engine struct {
	executable string
	run        func(ctx context.Context, executable string, args []string, timeout time.Duration) ([]byte, error)
}

func New(executable string) (*Engine, error) {
	resolved, err := exec.LookPath(executable)
	if err != nil || resolved == "" {
		return nil, fmt.Errorf("resolve %s: %w", executable, err)
	}
	return &Engine{executable: resolved, run: runCommand}, nil
}

// runCommand executes one bounded, deadline-bounded argv invocation. Output
// beyond the cap truncates and the call fails: unbounded engine output can
// never enter the process.
func runCommand(ctx context.Context, executable string, args []string, timeout time.Duration) ([]byte, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(execCtx, executable, args...)
	var stdout, stderr bytes.Buffer
	stdoutWriter := newBoundedWriter(&stdout, maxOutputBytes)
	stderrWriter := newBoundedWriter(&stderr, maxOutputBytes)
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	runErr := command.Run()
	if stdoutWriter.overflow || stderrWriter.overflow {
		return nil, fmt.Errorf("%w: engine output exceeded the bounded limit", ports.ErrEngineUnavailable)
	}
	if runErr != nil {
		if execCtx.Err() != nil {
			return nil, fmt.Errorf("%w: engine call timed out", ports.ErrEngineUnavailable)
		}
		exitErr := &exec.ExitError{}
		if errors.As(runErr, &exitErr) {
			return nil, fmt.Errorf("engine exited with code %d", exitErr.ExitCode())
		}
		return nil, fmt.Errorf("%w: %s", ports.ErrEngineUnavailable, engineFailure(runErr))
	}
	// stderr is deliberately bounded and discarded: raw engine error text
	// never crosses the adapter boundary into logs, errors, or responses.
	_ = stderr
	return stdout.Bytes(), nil
}

// engineFailure reduces an exec failure to a bounded fact: never the raw
// stderr, never a path beyond the executable's presence.
func engineFailure(err error) string {
	message := err.Error()
	if len(message) > 120 {
		message = message[:120]
	}
	return message
}

type boundedWriter struct {
	buffer   *bytes.Buffer
	remain   int
	overflow bool
}

func newBoundedWriter(buffer *bytes.Buffer, limit int) *boundedWriter {
	return &boundedWriter{buffer: buffer, remain: limit}
}

func (w *boundedWriter) Write(payload []byte) (int, error) {
	if w.remain <= 0 {
		w.overflow = true
		return len(payload), nil
	}
	if len(payload) > w.remain {
		w.buffer.Write(payload[:w.remain])
		w.remain = 0
		w.overflow = true
		return len(payload), nil
	}
	w.buffer.Write(payload)
	w.remain -= len(payload)
	return len(payload), nil
}

// infoDocument is the bounded subset of `podman info --format json` the
// capability verdict consumes. Unknown fields are ignored.
type infoDocument struct {
	Host struct {
		Architecture  string `json:"arch"`
		CgroupManager string `json:"cgroupManager"`
		CgroupVersion string `json:"cgroupVersion"`
		Security      struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
	} `json:"host"`
	Version struct {
		Version string `json:"Version"`
	} `json:"version"`
}

// Probe verifies rootless mode, cgroup v2, the delegated subtree, and the
// WorkOS internal network. Any miss is an honest false: the runtime refuses
// containers instead of degrading.
func (e *Engine) Probe(ctx context.Context) (ports.Capability, error) {
	output, err := e.run(ctx, e.executable, []string{"info", "--format", "json"}, probeTimeout)
	if err != nil {
		return ports.Capability{Available: false, Reason: "podman info failed"}, nil
	}
	var document infoDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return ports.Capability{Available: false, Reason: "podman info returned unreadable output"}, nil
	}
	if !document.Host.Security.Rootless {
		return ports.Capability{Available: false, Rootless: false,
			Reason: "podman is not running rootless"}, nil
	}
	if document.Host.CgroupVersion != "v2" {
		return ports.Capability{Available: false, Rootless: true,
			Reason: "cgroup v2 is not available"}, nil
	}
	// The delegated cgroup subtree must resolve from this process; the reader
	// validates every workload path against it.
	subtree, err := SelfSubtree()
	if err != nil {
		return ports.Capability{Available: false, Rootless: true, CgroupV2: true,
			Reason: "process cgroup subtree is unavailable"}, nil
	}
	// The WorkOS internal network must exist and be isolated; a failure here
	// is a capability miss, never a lazy per-launch failure.
	if err := e.ensureInternalNetwork(ctx); err != nil {
		return ports.Capability{Available: false, Rootless: true, CgroupV2: true,
			Reason: "workload network is unavailable"}, nil
	}
	return ports.Capability{
		Available: true, Rootless: true, CgroupV2: true,
		EngineVersion: document.Version.Version, CgroupRoot: subtree,
	}, nil
}

// ensureInternalNetwork guarantees the WorkOS network exists AND is internal
// (no external egress route). A same-named network that is not internal is a
// hostile or misconfigured host state: the capability verdict refuses
// containers instead of launching untrusted apps onto a routable network.
func (e *Engine) ensureInternalNetwork(ctx context.Context) error {
	output, err := e.run(ctx, e.executable, []string{"network", "ls", "--format", "{{.Name}}", "--filter", "name=" + networkName}, fastTimeout)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(name) == networkName {
			return e.verifyNetworkInternal(ctx)
		}
	}
	if _, err := e.run(ctx, e.executable, []string{"network", "create", "--internal", networkName}, fastTimeout); err != nil {
		return err
	}
	return e.verifyNetworkInternal(ctx)
}

func (e *Engine) verifyNetworkInternal(ctx context.Context) error {
	output, err := e.run(ctx, e.executable, []string{"network", "inspect", "--format", "{{.Internal}}", networkName}, fastTimeout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(output)) != "true" {
		return fmt.Errorf("network %s exists but is not internal", networkName)
	}
	return nil
}

func (e *Engine) ImageExists(ctx context.Context, image string) (bool, error) {
	if !domain.ValidImage(image) {
		return false, domain.ErrInvalid
	}
	_, err := e.run(ctx, e.executable, []string{"image", "exists", image}, fastTimeout)
	if err == nil {
		return true, nil
	}
	if exitedWithCode(err, 1) {
		return false, nil
	}
	return false, err
}

// CreateContainer translates the server-owned spec into one create argv. The
// security posture is fixed: no capabilities, no privilege escalation,
// read-only rootfs, one bounded noexec tmpfs, the WorkOS internal network,
// one random loopback-only published port, exact resource limits, and
// --restart=no (restart authority belongs to the supervisor, not the engine).
func (e *Engine) CreateContainer(ctx context.Context, spec ports.ContainerSpec) (string, error) {
	if spec.Name == "" || !domain.ValidImage(spec.Image) || !domain.ValidCommand(spec.Command) ||
		spec.Port < 1 || spec.Port > 65535 {
		return "", domain.ErrInvalid
	}
	if spec.Policy.CPUQuotaUSec < int64(domain.MinCPUHardCores*float64(domain.CPUPeriodUSec)) ||
		spec.Policy.MemoryHighBytes < domain.MinMemoryHighMB*1024*1024 ||
		spec.Policy.MemoryMaxBytes < spec.Policy.MemoryHighBytes || spec.Policy.PidsMax <= 0 ||
		spec.Policy.MemoryMaxBytes < domain.MinMemoryMaxMB*1024*1024 || spec.Policy.PidsMax < domain.MinPidsMax ||
		spec.Policy.CPUQuotaUSec > int64(domain.MaxCPUHardCores*float64(domain.CPUPeriodUSec)) ||
		spec.Policy.MemoryHighBytes > domain.MaxMemoryHighMB*1024*1024 ||
		spec.Policy.MemoryMaxBytes > domain.MaxMemoryMaxMB*1024*1024 ||
		spec.Policy.PidsMax > domain.MaxPidsMax {
		return "", domain.ErrInvalid
	}
	entrypoint, err := json.Marshal(spec.Command)
	if err != nil {
		return "", domain.ErrInvalid
	}
	args := []string{
		"create", "--name", spec.Name,
		"--pull=never",
		"--restart=no",
		"--image-volume=ignore",
		"--http-proxy=false",
		"--privileged=false",
		// Override both image ENTRYPOINT and its default CMD with one JSON exec
		// form. The resulting process argv is exactly the canonical command Core
		// resolved from the pinned manifest, including the one-element case.
		"--entrypoint", string(entrypoint),
		"--network", networkName,
		"--publish", fmt.Sprintf("127.0.0.1::%d", spec.Port),
		"--read-only",
		"--read-only-tmpfs=false",
		"--tmpfs", "/tmp:rw,size=33554432,noexec,nodev,nosuid",
		"--cap-drop=all",
		"--security-opt", "no-new-privileges",
		"--pids-limit", fmt.Sprintf("%d", spec.Policy.PidsMax),
		"--memory", fmt.Sprintf("%d", spec.Policy.MemoryMaxBytes),
		"--memory-reservation", fmt.Sprintf("%d", spec.Policy.MemoryHighBytes),
		"--cpu-period", fmt.Sprintf("%d", domain.CPUPeriodUSec),
		"--cpu-quota", fmt.Sprintf("%d", spec.Policy.CPUQuotaUSec),
	}
	labelKeys := make([]string, 0, len(spec.Labels))
	for key := range spec.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		value := spec.Labels[key]
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, "--")
	args = append(args, spec.Image)
	output, err := e.run(ctx, e.executable, args, startTimeout)
	if err != nil {
		// Exit 125 means a Podman-side error, not necessarily a name
		// collision. Only the dedicated existence command can prove that the
		// deterministic name was actually created by this or another attempt.
		if exitedWithCode(err, 125) {
			exists, existsErr := e.containerExists(ctx, spec.Name)
			if existsErr == nil && exists {
				return "", ports.ErrContainerAlreadyExists
			}
		}
		return "", err
	}
	containerID := strings.TrimSpace(string(output))
	if !validContainerID(containerID) {
		// Creation may have committed even when stdout is malformed. Surface an
		// unavailable verdict so reconciliation inspects the deterministic name;
		// never use an unbounded or ambiguous string as an engine identity.
		return "", fmt.Errorf("%w: engine returned an invalid container identity", ports.ErrEngineUnavailable)
	}
	return containerID, nil
}

func validContainerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (e *Engine) containerExists(ctx context.Context, nameOrID string) (bool, error) {
	_, err := e.run(ctx, e.executable, []string{"container", "exists", nameOrID}, fastTimeout)
	switch {
	case err == nil:
		return true, nil
	case exitedWithCode(err, 1):
		return false, nil
	default:
		// In particular, exit 125 means local storage could not be
		// inspected. It must remain unavailable, never collapse to absence.
		return false, err
	}
}

func (e *Engine) classifyContainerFailure(ctx context.Context, nameOrID string, operationErr error) error {
	if !exitedWithCode(operationErr, 125) {
		return operationErr
	}
	exists, err := e.containerExists(ctx, nameOrID)
	if err != nil {
		return err
	}
	if !exists {
		return ports.ErrContainerNotFound
	}
	return operationErr
}

func exitedWithCode(err error, code int) bool {
	return err != nil && err.Error() == fmt.Sprintf("engine exited with code %d", code)
}

func (e *Engine) StartContainer(ctx context.Context, nameOrID string) error {
	_, err := e.run(ctx, e.executable, []string{"start", nameOrID}, startTimeout)
	return e.classifyContainerFailure(ctx, nameOrID, err)
}

func (e *Engine) StopContainer(ctx context.Context, nameOrID string, timeout time.Duration) error {
	grace := int(timeout.Seconds())
	if grace < 1 {
		grace = 1
	}
	_, err := e.run(ctx, e.executable, []string{"stop", "-t", fmt.Sprintf("%d", grace), nameOrID}, timeout+startTimeout/2)
	return e.classifyContainerFailure(ctx, nameOrID, err)
}

func (e *Engine) RemoveContainer(ctx context.Context, nameOrID string) error {
	_, err := e.run(ctx, e.executable, []string{"rm", "-f", nameOrID}, fastTimeout)
	return e.classifyContainerFailure(ctx, nameOrID, err)
}

// containerDocument is the bounded subset of container inspect output the
// adapter consumes.
type containerDocument struct {
	ID            string          `json:"Id"`
	Name          string          `json:"Name"`
	ImageName     string          `json:"ImageName"`
	EffectiveCaps json.RawMessage `json:"EffectiveCaps"`
	BoundingCaps  json.RawMessage `json:"BoundingCaps"`
	State         struct {
		Running   bool `json:"Running"`
		ExitCode  int  `json:"ExitCode"`
		PID       int  `json:"Pid"`
		OOMKilled bool `json:"OOMKilled"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	Config struct {
		Labels     map[string]string `json:"Labels"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
		Privileged     bool              `json:"Privileged"`
		AutoRemove     bool              `json:"AutoRemove"`
		CapAdd         []string          `json:"CapAdd"`
		SecurityOpt    []string          `json:"SecurityOpt"`
		NetworkMode    string            `json:"NetworkMode"`
		Binds          []string          `json:"Binds"`
		Devices        []json.RawMessage `json:"Devices"`
		Tmpfs          map[string]string `json:"Tmpfs"`
		RestartPolicy  struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

func (e *Engine) InspectContainer(ctx context.Context, nameOrID string) (ports.ContainerFacts, error) {
	output, err := e.run(ctx, e.executable, []string{"container", "inspect", "--type", "container", nameOrID}, fastTimeout)
	if err != nil {
		return ports.ContainerFacts{}, e.classifyContainerFailure(ctx, nameOrID, err)
	}
	var documents []containerDocument
	if err := json.Unmarshal(output, &documents); err != nil || len(documents) != 1 {
		return ports.ContainerFacts{}, fmt.Errorf("engine inspect returned unreadable output")
	}
	document := documents[0]
	if !validContainerID(document.ID) {
		return ports.ContainerFacts{}, fmt.Errorf("engine inspect returned an invalid container identity")
	}
	effectiveCapabilities, err := capabilityCount(document.EffectiveCaps)
	if err != nil {
		return ports.ContainerFacts{}, fmt.Errorf("engine inspect returned unreadable effective capabilities")
	}
	boundingCapabilities, err := capabilityCount(document.BoundingCaps)
	if err != nil {
		return ports.ContainerFacts{}, fmt.Errorf("engine inspect returned unreadable bounding capabilities")
	}
	actualCommand := append([]string(nil), document.Config.Entrypoint...)
	actualCommand = append(actualCommand, document.Config.Cmd...)
	facts := ports.ContainerFacts{
		ID: document.ID, Name: document.Name,
		Running: document.State.Running, ExitCode: document.State.ExitCode,
		PID: document.State.PID, OOMKilled: document.State.OOMKilled,
		Labels: document.Config.Labels,
		Image:  document.ImageName, Command: actualCommand,
		ReadOnly: document.HostConfig.ReadonlyRootfs, Privileged: document.HostConfig.Privileged,
		AutoRemove:            document.HostConfig.AutoRemove,
		CapabilitiesAdded:     len(document.HostConfig.CapAdd),
		EffectiveCapabilities: effectiveCapabilities,
		BoundingCapabilities:  boundingCapabilities,
		NetworkMode:           document.HostConfig.NetworkMode, RestartPolicy: document.HostConfig.RestartPolicy.Name,
		BindMounts: len(document.HostConfig.Binds), Devices: len(document.HostConfig.Devices),
		Tmpfs: document.HostConfig.Tmpfs,
	}
	for _, option := range document.HostConfig.SecurityOpt {
		if option == "no-new-privileges" || option == "no-new-privileges=true" {
			facts.NoNewPrivileges = true
			continue
		}
		facts.UnexpectedSecurityOpts++
	}
	for _, mount := range document.Mounts {
		// Podman versions may or may not repeat the explicit /tmp tmpfs in
		// the top-level mount inventory. Every other mount (including an
		// anonymous volume inherited from the image) is forbidden.
		if mount.Type != "tmpfs" || mount.Destination != "/tmp" {
			facts.UnexpectedMounts++
		}
	}
	facts.ConnectedNetworks = len(document.NetworkSettings.Networks)
	_, facts.InternalNetwork = document.NetworkSettings.Networks[networkName]
	for containerPort, bindings := range document.NetworkSettings.Ports {
		parsedContainerPort, parsedContainerErr := parseContainerPort(containerPort)
		for _, binding := range bindings {
			facts.PublishedPorts++
			if facts.PublishedPorts > 1 {
				continue
			}
			if parsedContainerErr == nil {
				facts.ContainerPort = int32(parsedContainerPort)
			}
			facts.HostIP = binding.HostIP
			if parsed, err := parseDecimalPort(binding.HostPort); err == nil {
				facts.HostPort = int32(parsed)
			}
		}
	}
	return facts, nil
}

func capabilityCount(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("capability evidence is missing")
	}
	var capabilities []string
	if err := json.Unmarshal(raw, &capabilities); err != nil || capabilities == nil {
		return 0, errors.New("capability evidence is malformed")
	}
	return len(capabilities), nil
}

func parseContainerPort(value string) (int, error) {
	port, found := strings.CutSuffix(value, "/tcp")
	if !found {
		return 0, errors.New("container port is malformed")
	}
	return parseDecimalPort(port)
}

func parseDecimalPort(value string) (int, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("port is malformed")
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return 0, errors.New("port is malformed")
		}
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("port is malformed")
	}
	return port, nil
}

func (e *Engine) ListManagedContainers(ctx context.Context) ([]ports.ContainerFacts, error) {
	output, err := e.run(ctx, e.executable,
		[]string{"ps", "-a", "--filter", "label=" + managedLabel + "=workos", "--format", "{{.ID}}"}, fastTimeout)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	facts := make([]ports.ContainerFacts, 0, len(lines))
	for _, line := range lines {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		inspected, inspectErr := e.InspectContainer(ctx, id)
		if errors.Is(inspectErr, ports.ErrContainerNotFound) {
			continue
		}
		if inspectErr != nil {
			return nil, inspectErr
		}
		facts = append(facts, inspected)
	}
	return facts, nil
}

// UnavailableEngine is the honest no-engine adapter for hosts where the
// Podman executable cannot even be resolved. Every capability verdict is a
// fixed unavailable reason and every operation refuses with
// ErrEngineUnavailable: the runtime keeps serving DB-only facts and never
// fakes a launch or falls back to an unsafe engine.
type UnavailableEngine struct {
	reason string
}

func NewUnavailableEngine(reason string) *UnavailableEngine {
	if reason == "" {
		reason = "podman executable is not available"
	}
	return &UnavailableEngine{reason: reason}
}

func (e *UnavailableEngine) Probe(context.Context) (ports.Capability, error) {
	return ports.Capability{Available: false, Reason: e.reason}, nil
}

func (e *UnavailableEngine) ImageExists(context.Context, string) (bool, error) {
	return false, ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) CreateContainer(context.Context, ports.ContainerSpec) (string, error) {
	return "", ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) StartContainer(context.Context, string) error {
	return ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) StopContainer(context.Context, string, time.Duration) error {
	return ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) RemoveContainer(context.Context, string) error {
	return ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) InspectContainer(context.Context, string) (ports.ContainerFacts, error) {
	return ports.ContainerFacts{}, ports.ErrEngineUnavailable
}

func (e *UnavailableEngine) ListManagedContainers(context.Context) ([]ports.ContainerFacts, error) {
	return nil, ports.ErrEngineUnavailable
}
