package podman

import (
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
		return []byte("ctr-id-1"), nil
	})
	spec := testSpec()
	id, err := engine.CreateContainer(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if id != "ctr-id-1" {
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
		"--security-opt", "no-new-privileges",
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
	// The image reference and argv must be the trailing, unflagged arguments:
	// a manifest value can never be mistaken for an engine option.
	last := args[len(args)-3:]
	if last[0] != spec.Image || last[1] != spec.Command[0] || last[2] != spec.Command[1] {
		t.Fatalf("image/argv are not trailing operands: %v", last)
	}
	if calls[0].timeout <= 0 {
		t.Fatalf("engine call had no deadline")
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
	engine := newCapturingEngine(&calls, nil)
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
	}
	for index, mutate := range malformed {
		if _, err := engine.CreateContainer(context.Background(), mutate(base)); err == nil {
			t.Fatalf("malformed spec %d accepted", index)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("malformed specs reached exec: %v", calls)
	}

	// Leading-dash argv is inert by construction: the image and command are
	// trailing operands behind the `--` end-of-options marker, so a manifest
	// value can never be parsed as an engine option.
	if _, err := engine.CreateContainer(context.Background(), func(spec ports.ContainerSpec) ports.ContainerSpec {
		spec.Command = []string{"-o", "ProxyCommand=evil"}
		return spec
	}(base)); err != nil {
		t.Fatalf("dash argv rejected: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("dash argv call count %d, want 1", len(calls))
	}
	joined := strings.Join(calls[0].args, "")
	if !strings.Contains(joined, "--"+base.Image+"-o") {
		t.Fatalf("dash argv is not safely behind the -- marker: %s", joined)
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
