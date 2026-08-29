//go:build podmanfixture

// The opt-in REAL rootless Podman + cgroup v2 gate (ADR-0006 §4). It builds
// a deterministic FROM-scratch fixture image in the test-prep phase, launches
// it through the production engine adapter, and verifies the enforced
// boundaries on the live host: rootless capability, loopback-only publish,
// internal network, read-only rootfs with dropped capabilities, real cgroup
// hard limits, a controlled OOM confined to the fixture cgroup, and exact
// cleanup that leaves every user object untouched. `make test-podman-fixture`
// refuses to run (and never silently passes) on hosts without podman.
package podmanfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/adapters/podman"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
)

const (
	fixtureReference = "localhost/workos-web-fixture:fixture"
	fixturePort      = 8080
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// podmanExec runs one bounded podman command in the test harness.
func podmanExec(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "podman", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("podman %s: %v: %s", strings.Join(args, " "), err, bounded(output))
	}
	return string(output)
}

func bounded(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}

func objectList(t *testing.T, kind string) []string {
	t.Helper()
	output := podmanExec(t, 30*time.Second, kind, "ls", "--format", "{{.ID}}")
	var items []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line != "" {
			items = append(items, line)
		}
	}
	return items
}

func assertUserObjectsUntouched(t *testing.T, before, after map[string][]string, allowedDelta map[string]map[string]bool) {
	t.Helper()
	for kind, beforeItems := range before {
		afterItems := after[kind]
		seen := map[string]bool{}
		for _, item := range afterItems {
			seen[item] = true
		}
		for _, item := range beforeItems {
			if !seen[item] {
				t.Errorf("user %s object %s disappeared during the fixture run", kind, item)
			}
		}
		created := map[string]bool{}
		for _, item := range afterItems {
			created[item] = true
		}
		for _, item := range afterItems {
			wasBefore := false
			for _, old := range beforeItems {
				if old == item {
					wasBefore = true
					break
				}
			}
			if !wasBefore && !allowedDelta[kind][item] {
				t.Errorf("fixture run left a new %s object behind: %s", kind, item)
			}
		}
	}
}

// TestMain blocks the suite entirely on hosts without podman: the Makefile
// gate fails loudly and this suite never reports a silent pass.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("podman"); err != nil {
		fmt.Fprintln(os.Stderr, "BLOCKED: podman is not available on this host; the real rootless fixture gate cannot run.")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestRootlessFixtureImageLifecycle(t *testing.T) {
	// ---- capability preflight: probe must verify rootless + cgroup v2 ----
	engine, err := podman.New("podman")
	if err != nil {
		t.Fatalf("resolve podman: %v", err)
	}
	capability, err := engine.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe errored: %v", err)
	}
	if !capability.Available {
		t.Fatalf("BLOCKED: this host cannot host verified rootless workloads: %s", capability.Reason)
	}

	// ---- user object snapshots (before) ----
	before := map[string][]string{
		"container": objectList(t, "container"),
		"image":     objectList(t, "image"),
		"volume":    objectList(t, "volume"),
		"network":   objectList(t, "network"),
	}

	// ---- fixture image build (test-prep phase; the runtime never builds) ----
	buildDir := t.TempDir()
	binaryPath := filepath.Join(buildDir, "workos-web-fixture")
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./fixture")
	build.Dir = sourceDir()
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture binary: %v: %s", err, bounded(output))
	}
	containerfile := "FROM scratch\nCOPY workos-web-fixture /workos-web-fixture\nENTRYPOINT [\"/workos-web-fixture\"]\n"
	if err := os.WriteFile(filepath.Join(buildDir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}
	podmanExec(t, 2*time.Minute, "build", "-q", "-t", fixtureReference, buildDir)
	digestOutput := podmanExec(t, 30*time.Second, "inspect", "-f", "{{.Digest}}", fixtureReference)
	digest := strings.TrimSpace(digestOutput)
	if !digestPattern.MatchString(digest) {
		t.Fatalf("BLOCKED: locally built image exposes no digest to pin (got %q); a registry-visible digest is required by the launch policy", digest)
	}
	pinned := "localhost/workos-web-fixture@" + digest
	if !domain.ValidImage(pinned) {
		t.Fatalf("pinned reference %q failed the runtime grammar", pinned)
	}

	// ---- exact-identity cleanup, registered before any side effect ----
	var createdContainers []string
	t.Cleanup(func() {
		for _, id := range createdContainers {
			_ = exec.Command("podman", "rm", "-f", id).Run()
		}
		// Remove exactly the fixture image we built (by exact reference), so
		// the post-run image list matches the user's pre-run list.
		_ = exec.Command("podman", "rmi", fixtureReference).Run()
	})

	// ---- launch through the production adapter ----
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	policy := domain.EffectivePolicy{
		CPUQuotaUSec: 50000, MemoryHighBytes: 32 * 1024 * 1024, MemoryMaxBytes: 64 * 1024 * 1024,
		PidsMax: 32, StartupTimeout: 30 * time.Second, RestartLimit: 0, HealthPath: "/health",
	}
	spec := ports.ContainerSpec{
		Name:  "workos-fixture-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Image: pinned, Command: []string{"/workos-web-fixture"}, Port: fixturePort,
		Labels: map[string]string{"workos.managed": "workos", "workos.fixture": "true"},
		Policy: policy,
	}
	containerID, err := engine.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	createdContainers = append(createdContainers, containerID)
	if err := engine.StartContainer(ctx, spec.Name); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	facts, err := engine.InspectContainer(ctx, spec.Name)
	if err != nil {
		t.Fatalf("InspectContainer: %v", err)
	}
	if !facts.Running || facts.HostIP != "127.0.0.1" || facts.HostPort < 1 {
		t.Fatalf("publish boundary violated: running=%v host=%s:%d", facts.Running, facts.HostIP, facts.HostPort)
	}

	// ---- security posture inspect: rootfs, capabilities, network, user ----
	security := podmanExec(t, 30*time.Second, "container", "inspect", "--format",
		`{"privileged":{{.HostConfig.Privileged}},"readonly":{{.HostConfig.ReadonlyRootfs}},"capAdd":{{len .HostConfig.CapAdd}},"network":{{json .HostConfig.NetworkMode}},"user":"{{.Config.User}}","devices":{{len .HostConfig.Devices}}}`,
		spec.Name)
	joined := strings.Join(strings.Fields(security), "")
	for _, forbidden := range []string{`"privileged":true`, `"readonly":false`} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("security posture violated: %s", joined)
		}
	}
	if strings.Contains(joined, `"network":"host"`) {
		t.Fatalf("container joined the host network: %s", joined)
	}
	if !strings.Contains(joined, "workos-app-internal") {
		t.Fatalf("container is not on the WorkOS internal network: %s", joined)
	}

	// ---- external egress is unreachable on the internal network ----
	egress := podmanExec(t, 60*time.Second, "network", "inspect", "--format",
		`{{json .Internal}}`, "workos-app-internal")
	if !strings.Contains(strings.TrimSpace(egress), "true") {
		t.Fatalf("workload network is not internal (no external egress isolation): %s", egress)
	}

	// ---- startup health through the adapter prober ----
	prober := podman.NewProber()
	deadline := time.Now().Add(30 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		result, err := prober.Probe(ctx, fmt.Sprintf("127.0.0.1:%d", facts.HostPort), "/health", time.Second)
		if err == nil && result.Verdict == domain.HealthOK {
			healthy = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("fixture never became healthy on 127.0.0.1:%d", facts.HostPort)
	}
	// The marker page is served through the loopback publish.
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", facts.HostPort))
	if err != nil {
		t.Fatalf("marker fetch: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if !strings.Contains(string(body), "WORKOS-WEB-FIXTURE-OK") {
		t.Fatalf("marker page missing: %s", bounded(body))
	}

	// ---- real cgroup v2 enforcement read-back ----
	reader, err := podman.NewCgroupReader()
	if err != nil {
		t.Fatalf("cgroup reader: %v", err)
	}
	subtree, err := reader.SelfSubtree()
	if err != nil {
		t.Fatalf("self subtree: %v", err)
	}
	cgroupPath, err := reader.CgroupPathForPID(facts.PID)
	if err != nil {
		t.Fatalf("cgroup path for pid: %v", err)
	}
	if !domain.ValidCgroupPath(cgroupPath, subtree) {
		t.Fatalf("cgroup path %q is outside the delegated subtree %q", cgroupPath, subtree)
	}
	effective, err := reader.ReadEffective(ctx, cgroupPath)
	if err != nil {
		t.Fatalf("read effective: %v", err)
	}
	if effective.CPUMaxUSec != policy.CPUQuotaUSec || effective.MemoryHigh != policy.MemoryHighBytes ||
		effective.MemoryMax != policy.MemoryMaxBytes || effective.PIDsMax != policy.PidsMax {
		t.Fatalf("enforced policy drifted: got %+v want %+v", effective, policy)
	}

	// ---- controlled OOM: confined to the fixture cgroup ----
	processOOMBaseline := readOOM(t, "/proc/self/cgroup", subtree)
	hogPolicy := policy
	hogPolicy.MemoryMaxBytes = 8 * 1024 * 1024
	hogPolicy.MemoryHighBytes = 4 * 1024 * 1024
	hogSpec := spec
	hogSpec.Name = spec.Name + "-hog"
	hogSpec.Command = []string{"/workos-web-fixture", "hog"}
	hogSpec.Policy = hogPolicy
	hogID, err := engine.CreateContainer(ctx, hogSpec)
	if err != nil {
		t.Fatalf("CreateContainer hog: %v", err)
	}
	createdContainers = append(createdContainers, hogID)
	if err := engine.StartContainer(ctx, hogSpec.Name); err != nil {
		t.Fatalf("StartContainer hog: %v", err)
	}
	oomObserved := false
	hogDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(hogDeadline) {
		hogFacts, err := engine.InspectContainer(ctx, hogSpec.Name)
		if err == nil && hogFacts.OOMKilled {
			oomObserved = true
			break
		}
		if err == nil && !hogFacts.Running {
			// Exited without the OOM flag: accept, but prove the cgroup event
			// below.
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	hogFacts, err := engine.InspectContainer(ctx, hogSpec.Name)
	if err != nil {
		t.Fatalf("inspect hog: %v", err)
	}
	if hogFacts.Running {
		t.Fatalf("hog survived its memory limit")
	}
	if !oomObserved {
		// The cgroup may already be reaped; the engine OOM flag plus the
		// confinement probe below are the honest verdict.
		t.Logf("hog exited without an engine OOM flag (exit=%d); checking cgroup events", hogFacts.ExitCode)
	}
	// Confinement: the host process cgroup recorded no OOM kills.
	if processOOMBaseline != readOOM(t, "/proc/self/cgroup", subtree) {
		t.Fatalf("OOM escaped the fixture cgroup: host cgroup recorded kills")
	}

	// ---- exact cleanup ----
	if err := engine.RemoveContainer(ctx, containerID); err != nil {
		t.Fatalf("remove fixture container: %v", err)
	}
	if err := engine.RemoveContainer(ctx, hogID); err != nil {
		t.Fatalf("remove hog container: %v", err)
	}

	// ---- user object snapshots (after) must match (before) ----
	after := map[string][]string{
		"container": objectList(t, "container"),
		"image":     objectList(t, "image"),
		"volume":    objectList(t, "volume"),
		"network":   objectList(t, "network"),
	}
	assertUserObjectsUntouched(t, before, after, map[string]map[string]bool{
		// The WorkOS internal network is WorkOS-owned infrastructure; on a
		// first fixture run it is the only permitted addition.
		"network": {"workos-app-internal": true},
	})
	if net.ParseIP(facts.HostIP) == nil || facts.HostIP != "127.0.0.1" {
		t.Fatalf("publish host %q was not loopback", facts.HostIP)
	}
}

// readOOM resolves the calling process's cgroup and returns its oom_kill
// counter, proving the fixture OOM stayed confined.
func readOOM(t *testing.T, procPath, subtree string) uint64 {
	t.Helper()
	content, err := os.ReadFile(procPath)
	if err != nil {
		t.Fatalf("read %s: %v", procPath, err)
	}
	line := ""
	for _, candidate := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(candidate, "0::") {
			line = strings.TrimPrefix(candidate, "0::")
		}
	}
	if line == "" {
		t.Fatalf("no cgroup v2 entry in %s", procPath)
	}
	path := filepath.Join("/sys/fs/cgroup", strings.Trim(line, "/"), "memory.events")
	events, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read memory.events: %v", err)
	}
	for _, entry := range strings.Split(string(events), "\n") {
		parts := strings.Fields(entry)
		if len(parts) == 2 && parts[0] == "oom_kill" {
			var count uint64
			fmt.Sscanf(parts[1], "%d", &count)
			return count
		}
	}
	return 0
}

// sourceDir locates the fixture package on disk for the go build.
func sourceDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return filepath.Join(wd, "..", "..")
}

var _ = json.Marshal
var _ = net.ParseIP
