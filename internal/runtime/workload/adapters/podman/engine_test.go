package podman

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

// capturedCall records one engine invocation the adapter attempted.
type capturedCall struct {
	args    []string
	timeout time.Duration
}

var testContainerID = strings.Repeat("a", 64)

// newCapturingEngine returns an adapter whose exec layer is a capture
// function: no podman binary runs, and every argv assertion is exact.
func newCapturingEngine(calls *[]capturedCall, output func(args []string) ([]byte, error)) *Engine {
	engine, err := New("/usr/bin/podman")
	if err != nil {
		// New resolves the path; in the test environment /usr/bin/podman may
		// not exist, so construct directly with the injection point the
		// adapter exposes for exactly this seam.
		engine = &Engine{executable: "/usr/bin/podman"}
	}
	engine.run = func(_ context.Context, executable string, args []string, timeout time.Duration) ([]byte, error) {
		*calls = append(*calls, capturedCall{args: append([]string{executable}, args...), timeout: timeout})
		if output != nil {
			return output(args)
		}
		return []byte{}, nil
	}
	return engine
}

func testSpec() ports.ContainerSpec {
	return ports.ContainerSpec{
		Name:    "workos-wl-0198d7ea-2110-7c42-b659-c5e4d73bc361",
		Image:   "localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Command: []string{"/workos-fixture", "serve"}, Port: 8080,
		Labels: map[string]string{"workos.managed": "workos", "workos.workload.id": "wl"},
		Policy: domain.EffectivePolicy{
			CPUQuotaUSec: 100000, MemoryHighBytes: 64 * 1024 * 1024,
			MemoryMaxBytes: 96 * 1024 * 1024, PidsMax: 32,
			StartupTimeout: 10 * time.Second, RestartLimit: 2, HealthPath: "/health",
		},
	}
}

// TestCreateContainerArgvIsExactAndShellFree pins the launch argv: every
// security and resource flag is present, the image is digest-pinned with
// pull=never, and no shell is ever involved.
func TestCreateContainerArgvIsExactAndShellFree(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
		return []byte(testContainerID), nil
	})
	spec := testSpec()
	id, err := engine.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if id != testContainerID {
		t.Fatalf("container id %q", id)
	}
	if len(calls) != 1 {
		t.Fatalf("calls %d, want 1", len(calls))
	}
	args := calls[0].args
	joined := strings.Join(args, "\x1f")
	if args[0] != "/usr/bin/podman" {
		t.Fatalf("executable %q is not the resolved absolute path", args[0])
	}
	for _, forbidden := range []string{"sh", "bash", "-c", "/bin/sh"} {
		for _, arg := range args[1:] {
			if arg == forbidden {
				t.Fatalf("argv contains shell fragment %q", forbidden)
			}
		}
	}
	for _, required := range []string{
		"--pull=never", "--restart=no", "--read-only", "--cap-drop=all",
		"--image-volume=ignore", "--http-proxy=false", "--privileged=false",
		"--security-opt", "no-new-privileges",
		"--entrypoint\x1f[\"/workos-fixture\",\"serve\"]",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("argv missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "0.0.0.0") || strings.Contains(joined, "--network=host") {
		t.Fatalf("argv publishes beyond loopback: %s", joined)
	}
	if !strings.Contains(joined, "127.0.0.1::8080") {
		t.Fatalf("argv missing the loopback random publish: %s", joined)
	}
	if !strings.Contains(joined, "--pids-limit\x1f32") || !strings.Contains(joined, "--memory\x1f"+itoa(96*1024*1024)) {
		t.Fatalf("argv missing resource limits: %s", joined)
	}
	// The complete executable argv is an explicit JSON entrypoint override.
	// Nothing follows the image, so an image-defined entrypoint or CMD cannot
	// alter the canonical argv.
	if args[len(args)-1] != spec.Image || args[len(args)-2] != "--" {
		t.Fatalf("image is not the sole trailing operand: %v", args[len(args)-2:])
	}
	if calls[0].timeout <= 0 {
		t.Fatalf("engine call had no deadline")
	}
}

func TestCreateContainerOneElementCommandOverridesImageDefaults(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return []byte(testContainerID), nil
	})
	spec := testSpec()
	spec.Command = []string{"/workos-fixture"}
	if _, err := engine.CreateContainer(context.Background(), spec); err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls %d, want 1", len(calls))
	}
	joined := strings.Join(calls[0].args, "\x1f")
	if !strings.Contains(joined, "--entrypoint\x1f[\"/workos-fixture\"]") ||
		!strings.HasSuffix(joined, "--\x1f"+spec.Image) {
		t.Fatalf("single-element argv did not replace image defaults: %s", joined)
	}
}

func TestCreateContainerRejectsMalformedEngineIdentity(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return []byte("not-a-container-id"), nil
	})
	if _, err := engine.CreateContainer(context.Background(), testSpec()); !errors.Is(err, ports.ErrEngineUnavailable) {
		t.Fatalf("malformed identity verdict %v, want unavailable", err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestCreateContainerRejectsMalformedSpecs pins the fail-closed grammar: a
// tag-bearing image, unbounded argv, or a zero policy never reach exec.
func TestCreateContainerRejectsMalformedSpecs(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return []byte(testContainerID), nil
	})
	base := testSpec()

	malformed := []func(ports.ContainerSpec) ports.ContainerSpec{
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Image = "localhost/workos-fixture:latest"
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Image = "localhost/workos-fixture@sha256:SHORT"
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Command = nil
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Policy.PidsMax = 0
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Policy.PidsMax = domain.MinPidsMax - 1
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Policy.CPUQuotaUSec = int64(domain.MinCPUHardCores*float64(domain.CPUPeriodUSec)) - 1
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Port = 65536
			return spec
		},
		func(spec ports.ContainerSpec) ports.ContainerSpec {
			spec.Policy.MemoryMaxBytes = (domain.MaxMemoryMaxMB + 1) * 1024 * 1024
			return spec
		},
	}
	for index, mutate := range malformed {
		if _, err := engine.CreateContainer(context.Background(), mutate(base)); err == nil {
			t.Fatalf("malformed spec %d accepted", index)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("malformed specs reached exec: %v", calls)
	}

	// Leading-dash argv is encoded as a JSON exec-form entrypoint value; the
	// image remains the sole operand behind `--`. No manifest element can be
	// parsed as an engine option.
	if _, err := engine.CreateContainer(context.Background(), func(spec ports.ContainerSpec) ports.ContainerSpec {
		spec.Command = []string{"-o", "ProxyCommand=evil"}
		return spec
	}(base)); err != nil {
		t.Fatalf("dash argv rejected: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("dash argv call count %d, want 1", len(calls))
	}
	joined := strings.Join(calls[0].args, "\x1f")
	if !strings.Contains(joined, "--entrypoint\x1f[\"-o\",\"ProxyCommand=evil\"]") ||
		!strings.HasSuffix(joined, "--\x1f"+base.Image) {
		t.Fatalf("dash argv is not safely separated from engine options: %s", joined)
	}
}

// rootlessProbeInfo is a capable `podman info` document for the probe tests.
var rootlessProbeInfo = []byte(`{"host":{"cgroupVersion":"v2","security":{"rootless":true}},"version":{"Version":"5.0.0"}}`)

// TestProbeVerifiesRootlessAndCgroupV2 pins the capability verdicts: rootful
// engines, cgroup v1 hosts, and unreadable output are all honestly false.
func TestProbeVerifiesRootlessAndCgroupV2(t *testing.T) {
	rootlessInfo := []byte(`{"host":{"cgroupVersion":"v2","cgroupManager":"systemd","security":{"rootless":true}},"version":{"Version":"5.0.0"}}`)
	rootfulInfo := []byte(`{"host":{"cgroupVersion":"v2","security":{"rootless":false}},"version":{"Version":"5.0.0"}}`)
	cgroupV1Info := []byte(`{"host":{"cgroupVersion":"v1","security":{"rootless":true}},"version":{"Version":"5.0.0"}}`)

	networkArgs := []string{"network", "ls"}
	cases := []struct {
		name       string
		output     []byte
		outputErr  error
		wantOK     bool
		wantReason string
	}{
		{"rootless cgroup v2", rootlessInfo, nil, true, ""},
		{"rootful engine", rootfulInfo, nil, false, "not running rootless"},
		{"cgroup v1", cgroupV1Info, nil, false, "cgroup v2"},
		{"bad json", []byte("{not-json"), nil, false, "unreadable"},
		{"engine failure", nil, errors.New("boom"), false, "info failed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var calls []capturedCall
			engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
				if sameArgs(args[1:], networkArgs) {
					return []byte("workos-app-internal\n"), nil
				}
				if strings.Contains(strings.Join(args, " "), "network inspect") {
					return []byte("true\n"), nil
				}
				return testCase.output, testCase.outputErr
			})
			capability, err := engine.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe returned an error: %v", err)
			}
			if capability.Available != testCase.wantOK {
				t.Fatalf("capability %v (reason %q), want %v", capability.Available, capability.Reason, testCase.wantOK)
			}
			if testCase.wantReason != "" && !strings.Contains(capability.Reason, testCase.wantReason) {
				t.Fatalf("reason %q does not mention %q", capability.Reason, testCase.wantReason)
			}
		})
	}
}

// TestProbeRefusesNonInternalNetwork pins the egress boundary: a same-named
// network that is not internal is a hard capability refusal, and the adapter
// never recreates it silently.
func TestProbeRefusesNonInternalNetwork(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
		switch {
		case strings.Contains(strings.Join(args, " "), "network ls"):
			return []byte("workos-app-internal\n"), nil
		case strings.Contains(strings.Join(args, " "), "network inspect"):
			return []byte("false\n"), nil
		default:
			return rootlessProbeInfo, nil
		}
	})
	capability, err := engine.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if capability.Available {
		t.Fatalf("non-internal network accepted")
	}
	if !strings.Contains(capability.Reason, "workload network") {
		t.Fatalf("reason %q does not name the network boundary", capability.Reason)
	}
	for _, call := range calls {
		if strings.Contains(strings.Join(call.args, " "), "network create") {
			t.Fatalf("adapter recreated the foreign network instead of refusing")
		}
	}
}

func TestProbeDoesNotMistakeNetworkNamePrefixForExactNetwork(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "network ls"):
			return []byte(networkName + "-foreign\n"), nil
		case strings.Contains(joined, "network create"):
			return []byte(networkName), nil
		case strings.Contains(joined, "network inspect"):
			return []byte("true\n"), nil
		default:
			return rootlessProbeInfo, nil
		}
	})
	capability, err := engine.Probe(context.Background())
	if err != nil || !capability.Available {
		t.Fatalf("capability=%+v err=%v", capability, err)
	}
	created := false
	for _, call := range calls {
		if strings.Contains(strings.Join(call.args, " "), "network create") {
			created = true
		}
	}
	if !created {
		t.Fatal("prefix-matching foreign network suppressed exact network creation")
	}
}

func sameArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// TestNewRequiresResolvableBinary pins that a missing podman is a
// construction failure, never a lazy first-launch failure.
func TestNewRequiresResolvableBinary(t *testing.T) {
	if _, err := New("workos-definitely-not-a-binary"); err == nil {
		t.Fatalf("missing binary accepted")
	}
}

func TestInspectAndManagedListCarryAdoptionSecurityFacts(t *testing.T) {
	var calls []capturedCall
	spec := testSpec()
	inspectJSON := `[{"Id":"` + testContainerID + `","Name":"` + spec.Name + `","ImageName":"` + spec.Image + `",` +
		`"State":{"Running":true,"ExitCode":0,"Pid":4242,"OOMKilled":false},"EffectiveCaps":[],"BoundingCaps":[],"Mounts":[],` +
		`"Config":{"Labels":{"workos.managed":"workos","workos.workload.id":"wl"},"Entrypoint":["/workos-fixture","serve"],"Cmd":null},` +
		`"HostConfig":{"ReadonlyRootfs":true,"Privileged":false,"CapAdd":[],"CapDrop":["ALL"],"SecurityOpt":["no-new-privileges"],` +
		`"NetworkMode":"workos-app-internal","Binds":[],"Devices":[],"Tmpfs":{"/tmp":"rw,noexec,nodev,nosuid"},` +
		`"RestartPolicy":{"Name":"no"}},` +
		`"NetworkSettings":{"Ports":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"41001"}]},` +
		`"Networks":{"workos-app-internal":{}}}}]`
	engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "ps" {
			return []byte("ctr-1\n"), nil
		}
		return []byte(inspectJSON), nil
	})
	facts, err := engine.InspectContainer(context.Background(), spec.Name)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if facts.Image != spec.Image || !facts.ReadOnly || facts.EffectiveCapabilities != 0 || facts.BoundingCapabilities != 0 || !facts.NoNewPrivileges ||
		facts.NetworkMode != networkName || facts.RestartPolicy != "no" || len(facts.Command) != 2 ||
		facts.ContainerPort != int32(spec.Port) || facts.PublishedPorts != 1 ||
		facts.ConnectedNetworks != 1 || !facts.InternalNetwork || facts.UnexpectedSecurityOpts != 0 || facts.AutoRemove {
		t.Fatalf("inspect adoption facts incomplete: %+v", facts)
	}
	managed, err := engine.ListManagedContainers(context.Background())
	if err != nil || len(managed) != 1 || managed[0].Labels[managedLabel] != "workos" {
		t.Fatalf("managed facts=%+v err=%v", managed, err)
	}
	if managed[0].Image != spec.Image {
		t.Fatalf("managed list did not inspect the exact container: %+v", managed[0])
	}
}

func TestInspectPreservesUnexpectedSecurityAndNetworkFacts(t *testing.T) {
	var calls []capturedCall
	inspectJSON := `[{"Id":"` + testContainerID + `","Name":"ctr","ImageName":"image","State":{},` +
		`"EffectiveCaps":["CAP_NET_RAW"],"BoundingCaps":["CAP_CHOWN"],"Mounts":[],` +
		`"Config":{},"HostConfig":{"SecurityOpt":["no-new-privileges","seccomp=unconfined"],"AutoRemove":true},` +
		`"NetworkSettings":{"Ports":{"08080/tcp":[{"HostIp":"127.0.0.1","HostPort":"41001x"}]},` +
		`"Networks":{"workos-app-internal":{},"external":{}}}}]`
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) { return []byte(inspectJSON), nil })
	facts, err := engine.InspectContainer(context.Background(), "ctr")
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if !facts.NoNewPrivileges || facts.UnexpectedSecurityOpts != 1 || !facts.AutoRemove ||
		facts.EffectiveCapabilities != 1 || facts.BoundingCapabilities != 1 ||
		facts.ConnectedNetworks != 2 || !facts.InternalNetwork {
		t.Fatalf("security/network drift was hidden: %+v", facts)
	}
	if facts.ContainerPort != 0 || facts.HostPort != 0 || facts.PublishedPorts != 1 {
		t.Fatalf("non-canonical port text was partially accepted: %+v", facts)
	}
}

func TestInspectReportsUnexpectedImageVolume(t *testing.T) {
	var calls []capturedCall
	inspectJSON := `[{"Id":"` + testContainerID + `","Name":"ctr","ImageName":"image",` +
		`"State":{},"EffectiveCaps":[],"BoundingCaps":[],"Config":{},"HostConfig":{},"NetworkSettings":{"Ports":{}},` +
		`"Mounts":[{"Type":"tmpfs","Destination":"/tmp"},{"Type":"volume","Destination":"/data"}]}]`
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return []byte(inspectJSON), nil
	})
	facts, err := engine.InspectContainer(context.Background(), "ctr")
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if facts.UnexpectedMounts != 1 {
		t.Fatalf("unexpected mounts %d, want 1", facts.UnexpectedMounts)
	}
}

func TestInspectRejectsMissingActualCapabilityEvidence(t *testing.T) {
	var calls []capturedCall
	inspectJSON := `[{"Id":"` + testContainerID + `","Name":"ctr","ImageName":"image",` +
		`"State":{},"Config":{},"HostConfig":{},"NetworkSettings":{"Ports":{}},"Mounts":[]}]`
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return []byte(inspectJSON), nil
	})
	if _, err := engine.InspectContainer(context.Background(), "ctr"); err == nil {
		t.Fatal("inspect accepted missing actual capability evidence")
	}
}

func TestStopAndRemoveMapDisappearedContainer(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "container" && args[1] == "exists" {
			return nil, errors.New("engine exited with code 1")
		}
		return nil, errors.New("engine exited with code 125")
	})
	if err := engine.StopContainer(context.Background(), "ctr", time.Second); !errors.Is(err, ports.ErrContainerNotFound) {
		t.Fatalf("stop verdict %v, want not found", err)
	}
	if err := engine.RemoveContainer(context.Background(), "ctr"); !errors.Is(err, ports.ErrContainerNotFound) {
		t.Fatalf("remove verdict %v, want not found", err)
	}
	if _, err := engine.InspectContainer(context.Background(), "ctr"); !errors.Is(err, ports.ErrContainerNotFound) {
		t.Fatalf("inspect verdict %v, want not found", err)
	}
}

func TestContainerStorageFailureNeverMapsToAbsent(t *testing.T) {
	var calls []capturedCall
	engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
		return nil, errors.New("engine exited with code 125")
	})
	for name, invoke := range map[string]func() error{
		"start":  func() error { return engine.StartContainer(context.Background(), "ctr") },
		"stop":   func() error { return engine.StopContainer(context.Background(), "ctr", time.Second) },
		"remove": func() error { return engine.RemoveContainer(context.Background(), "ctr") },
		"inspect": func() error {
			_, err := engine.InspectContainer(context.Background(), "ctr")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := invoke()
			if err == nil || errors.Is(err, ports.ErrContainerNotFound) {
				t.Fatalf("storage failure collapsed to absence: %v", err)
			}
		})
	}
}

func TestImageExistsDistinguishesAbsentFromEngineFailure(t *testing.T) {
	spec := testSpec()
	for _, testCase := range []struct {
		name      string
		exitCode  string
		wantFound bool
		wantError bool
	}{
		{name: "absent", exitCode: "1", wantFound: false, wantError: false},
		{name: "storage failure", exitCode: "125", wantFound: false, wantError: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var calls []capturedCall
			engine := newCapturingEngine(&calls, func([]string) ([]byte, error) {
				return nil, errors.New("engine exited with code " + testCase.exitCode)
			})
			found, err := engine.ImageExists(context.Background(), spec.Image)
			if found != testCase.wantFound || (err != nil) != testCase.wantError {
				t.Fatalf("found=%v err=%v", found, err)
			}
		})
	}
}

func TestCreateExit125RequiresProvenNameCollision(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		existsErr  error
		wantExists bool
	}{
		{name: "name exists", wantExists: true},
		{name: "storage unavailable", existsErr: errors.New("engine exited with code 125")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var calls []capturedCall
			engine := newCapturingEngine(&calls, func(args []string) ([]byte, error) {
				if len(args) >= 2 && args[0] == "container" && args[1] == "exists" {
					return nil, testCase.existsErr
				}
				return nil, errors.New("engine exited with code 125")
			})
			_, err := engine.CreateContainer(context.Background(), testSpec())
			if errors.Is(err, ports.ErrContainerAlreadyExists) != testCase.wantExists {
				t.Fatalf("create verdict %v, want collision=%v", err, testCase.wantExists)
			}
		})
	}
}

func TestBoundedWriterMarksTruncationAsFailureEvidence(t *testing.T) {
	var buffer bytes.Buffer
	writer := newBoundedWriter(&buffer, 3)
	if count, err := writer.Write([]byte("ab")); err != nil || count != 2 {
		t.Fatalf("first write count=%d err=%v", count, err)
	}
	if count, err := writer.Write([]byte("cd")); err != nil || count != 2 {
		t.Fatalf("overflow write count=%d err=%v", count, err)
	}
	if buffer.String() != "abc" || !writer.overflow {
		t.Fatalf("bounded writer buffer=%q overflow=%v", buffer.String(), writer.overflow)
	}
}
