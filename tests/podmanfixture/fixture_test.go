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

const fixturePort = 8080

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
	format := "{{.ID}}"
	if kind == "network" {
		// The one permitted fixture delta is the named WorkOS internal
		// network, so network snapshots use names rather than opaque IDs.
		format = "{{.Name}}"
	}
	output := podmanExec(t, 30*time.Second, kind, "ls", "--format", format)
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
	fixtureReference := fmt.Sprintf("localhost/workos-web-fixture:fixture-%d", time.Now().UnixNano())
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
	// Register exact cleanup before the image build, the first fixture-owned
	// side effect. Every early failure after this point removes only the
	// unique tag and exact container IDs created by this run.
	var createdContainers []string
	t.Cleanup(func() {
		for _, id := range createdContainers {
			_ = exec.Command("podman", "rm", "-f", id).Run()
		}
		_ = exec.Command("podman", "rmi", fixtureReference).Run()
	})

	// ---- fixture image build (test-prep phase; the runtime never builds) ----
	// The payload binary arrives prebuilt (the Makefile compiles it in the
	// toolchain container and hands it over via WORKOS_PODMAN_FIXTURE_BINARY,
	// so no Go toolchain is needed on the podman host); a host-native run
	// with a Go toolchain may build it as a fallback.
	binaryPath := os.Getenv("WORKOS_PODMAN_FIXTURE_BINARY")
	if binaryPath == "" {
		if _, err := exec.LookPath("go"); err != nil {
			t.Skipf("BLOCKED: no prebuilt fixture binary (WORKOS_PODMAN_FIXTURE_BINARY) and no host Go toolchain to build it: %v", err)
		}
		binaryPath = filepath.Join(t.TempDir(), "workos-web-fixture")
		build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./fixture")
		build.Dir = sourceDir()
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fixture binary: %v: %s", err, bounded(output))
		}
	}
	if _, err := os.Stat(binaryPath); err != nil {
		t.Fatalf("prebuilt fixture binary: %v", err)
	}
	// The image build context contains exactly the Containerfile and the
	// payload under the deterministic name the Containerfile copies.
	buildDir := t.TempDir()
	staged := filepath.Join(buildDir, "workos-web-fixture")
	payload, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read fixture binary: %v", err)
	}
	if err := os.WriteFile(staged, payload, 0o755); err != nil {
		t.Fatalf("stage fixture binary: %v", err)
	}
	// Deliberately hostile image defaults prove that the production adapter's
	// JSON entrypoint override produces only the canonical manifest argv.
	containerfile := "FROM scratch\nCOPY workos-web-fixture /workos-web-fixture\n" +
		"ENTRYPOINT [\"/image-entrypoint-must-be-overridden\"]\n" +
		"CMD [\"image-cmd-must-be-overridden\"]\n"
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
	if !facts.Running || facts.HostIP != "127.0.0.1" || facts.HostPort < 1 ||
		facts.ContainerPort != fixturePort || facts.PublishedPorts != 1 {
		t.Fatalf("publish boundary violated: running=%v host=%s:%d", facts.Running, facts.HostIP, facts.HostPort)
	}
	if facts.Image != pinned || len(facts.Command) != 1 || facts.Command[0] != "/workos-web-fixture" ||
		!facts.ReadOnly || facts.Privileged || facts.CapabilitiesAdded != 0 ||
		facts.EffectiveCapabilities != 0 || facts.BoundingCapabilities != 0 || !facts.NoNewPrivileges ||
		facts.UnexpectedSecurityOpts != 0 || facts.AutoRemove || facts.NetworkMode != "workos-app-internal" ||
		facts.ConnectedNetworks != 1 || !facts.InternalNetwork || facts.RestartPolicy != "no" ||
		facts.BindMounts != 0 || facts.UnexpectedMounts != 0 || facts.Devices != 0 || len(facts.Tmpfs) != 1 ||
		!exactTmpfsOptions(facts.Tmpfs["/tmp"]) {
		t.Fatalf("production inspect did not prove the immutable security profile: %+v", facts)
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
	if effective.CPUMaxUSec != policy.CPUQuotaUSec || effective.CPUPeriodUSec != domain.CPUPeriodUSec ||
		effective.MemoryHigh != policy.MemoryHighBytes ||
		effective.MemoryMax != policy.MemoryMaxBytes || effective.PIDsMax != policy.PidsMax {
		t.Fatalf("enforced policy drifted: got %+v want %+v", effective, policy)
	}

	// ---- controlled OOM: confined to the fixture cgroup ----
	processOOMBaseline := readOOM(t, "/proc/self/cgroup", subtree)
	hogPolicy := policy
	hogPolicy.MemoryMaxBytes = 32 * 1024 * 1024
	hogPolicy.MemoryHighBytes = 16 * 1024 * 1024
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
	hogFacts, err := engine.InspectContainer(ctx, hogSpec.Name)
	if err != nil || hogFacts.PID <= 0 {
		t.Fatalf("inspect running hog: facts=%+v err=%v", hogFacts, err)
	}
	hogCgroup, err := reader.CgroupPathForPID(hogFacts.PID)
	if err != nil {
		t.Fatalf("hog cgroup path: %v", err)
	}
	oomObserved := false
	hogDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(hogDeadline) {
		hogFacts, err = engine.InspectContainer(ctx, hogSpec.Name)
		if err == nil && hogFacts.OOMKilled {
			oomObserved = true
		}
		if counters, readErr := reader.ReadCounters(ctx, hogCgroup); readErr == nil && counters.MemoryOOMs > 0 {
			oomObserved = true
		}
		if err == nil && !hogFacts.Running {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	hogFacts, err = engine.InspectContainer(ctx, hogSpec.Name)
	if err != nil {
		t.Fatalf("inspect hog: %v", err)
	}
	if hogFacts.Running {
		t.Fatalf("hog survived its memory limit")
	}
	if !oomObserved {
		t.Fatalf("hog exited without an engine OOM verdict or memory.events oom increment (exit=%d)", hogFacts.ExitCode)
	}
	// Confinement: the host process cgroup recorded no OOM kills.
	if processOOMBaseline != readOOM(t, "/proc/self/cgroup", subtree) {
		t.Fatalf("OOM escaped the fixture cgroup: host cgroup recorded kills")
	}

	// ---- controlled pids.max event: rejected inside the fixture cgroup ----
	pidsSpec := spec
	pidsSpec.Name = spec.Name + "-pids"
	pidsSpec.Command = []string{"/workos-web-fixture", "pids"}
	pidsSpec.Policy.PidsMax = 16
	pidsID, err := engine.CreateContainer(ctx, pidsSpec)
	if err != nil {
		t.Fatalf("CreateContainer pids: %v", err)
	}
	createdContainers = append(createdContainers, pidsID)
	if err := engine.StartContainer(ctx, pidsSpec.Name); err != nil {
		t.Fatalf("StartContainer pids: %v", err)
	}
	pidsFacts, err := engine.InspectContainer(ctx, pidsSpec.Name)
	if err != nil {
		t.Fatalf("inspect pids fixture: %v", err)
	}
	pidsCgroup, err := reader.CgroupPathForPID(pidsFacts.PID)
	if err != nil {
		t.Fatalf("pids cgroup path: %v", err)
	}
	pidsObserved := false
	pidsDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(pidsDeadline) {
		counters, readErr := reader.ReadCounters(ctx, pidsCgroup)
		if readErr == nil && counters.PIDsLimitEvents > 0 {
			pidsObserved = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !pidsObserved {
		t.Fatal("pids.max rejection did not increment pids.events max")
	}

	// ---- exact cleanup ----
	if err := engine.RemoveContainer(ctx, containerID); err != nil {
		t.Fatalf("remove fixture container: %v", err)
	}
	if err := engine.RemoveContainer(ctx, hogID); err != nil {
		t.Fatalf("remove hog container: %v", err)
	}
	if err := engine.RemoveContainer(ctx, pidsID); err != nil {
		t.Fatalf("remove pids container: %v", err)
	}
	// The after snapshot is part of the assertion, so cleanup must happen
	// before it rather than only in t.Cleanup. The unique tag prevents the
	// fixture from replacing or removing a user's pre-existing image tag.
	podmanExec(t, 30*time.Second, "rmi", fixtureReference)

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

func exactTmpfsOptions(options string) bool {
	required := map[string]bool{
		"rw": false, "size=33554432": false, "noexec": false, "nodev": false, "nosuid": false,
	}
	parts := strings.Split(options, ",")
	if len(parts) != len(required) {
		return false
	}
	for _, option := range parts {
		if _, ok := required[option]; !ok || required[option] {
			return false
		}
		required[option] = true
	}
	return true
}

// sourceDir locates the fixture package on disk for the go build.
func sourceDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for _, candidate := range []string{
		wd,
		filepath.Join(wd, "tests", "podmanfixture"),
		filepath.Join(wd, "..", "..", "tests", "podmanfixture"),
	} {
		if _, err := os.Stat(filepath.Join(candidate, "fixture", "main.go")); err == nil {
			return candidate
		}
	}
	return wd
}
