package dockerapp

import (
	"context"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/runtime/workload/domain"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const probeImage = "golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"

func realDockerApp(t *testing.T) *Engine {
	t.Helper()
	if os.Getenv("WORKOS_TEST_DOCKER_BUILD") == "" {
		t.Skip("WORKOS_TEST_DOCKER_BUILD not set")
	}
	socket := os.Getenv("WORKOS_TEST_DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("docker socket: %v", err)
	}
	engine, err := New(Config{Socket: socket, UnpackRoot: hostScratch(t)}, fakeBundles{})
	if err != nil {
		t.Fatal(err)
	}
	capability, err := engine.Probe(context.Background())
	if err != nil || !capability.Available {
		t.Fatalf("docker unavailable: %v %+v", err, capability)
	}
	return engine
}

func hostScratch(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			tmp := filepath.Join(dir, "tmp")
			_ = os.MkdirAll(tmp, 0o755)
			scratch, mkErr := os.MkdirTemp(tmp, "dockerapp-real-")
			if mkErr != nil {
				t.Fatal(mkErr)
			}
			t.Cleanup(func() { _ = os.RemoveAll(scratch) })
			return scratch
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return t.TempDir()
}

func TestRealDockerAppServesDistinctBundles(t *testing.T) {
	engine := realDockerApp(t)
	root := hostScratch(t)
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	source := `package main
import ("fmt"; "net/http")
func main() {
  http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "WORKOS-MARK") })
  http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
  _ = http.ListenAndServe("0.0.0.0:8080", nil)
}`
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	build := func(mark, dest string) {
		t.Helper()
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("docker", "run", "--rm", "--pull=never", "--network", "none",
			"-v", src+":/src", "-v", dest+":/out", "-w", "/src",
			"-e", "CGO_ENABLED=0", "-e", "GOPROXY=off", "-e", "GOCACHE=/tmp/gocache",
			probeImage, "sh", "-c", "sed 's/WORKOS-MARK/"+mark+"/' main.go > build.go && go build -o /out/server build.go")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", mark, err, out)
		}
	}
	bundleA := filepath.Join(root, "a")
	bundleB := filepath.Join(root, "b")
	build("WORKOS-P3-A", bundleA)
	build("WORKOS-P3-B", bundleB)
	pack := func(dir string) (string, string) {
		t.Helper()
		tarPath := dir + ".tar"
		handle, err := os.Create(tarPath)
		if err != nil {
			t.Fatal(err)
		}
		stats, err := appbundle.EncodeDirectory(dir, handle)
		handle.Close()
		if err != nil {
			t.Fatal(err)
		}
		return tarPath, stats.Digest
	}
	tarA, digestA := pack(bundleA)
	tarB, digestB := pack(bundleB)
	if digestA == digestB {
		t.Fatal("A and B bundles must differ")
	}
	engine.bundles = fakeBundles{path: tarA}
	ctx := context.Background()
	start := func(name, digest, tar string) string {
		t.Helper()
		engine.bundles = fakeBundles{path: tar}
		id, err := engine.CreateContainer(ctx, ports.ContainerSpec{
			Name: name, Image: probeImage, Command: []string{"/app/server"}, Port: 8080,
			OwnerUserID: "01999999-9999-7999-8999-000000000c01", ArtifactID: "01999999-9999-7999-8999-000000000c02", ArtifactDigest: digest,
			Policy: domain.EffectivePolicy{MemoryMaxBytes: 256 << 20, PidsMax: 64, CPUQuotaUSec: 150000},
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = engine.RemoveContainer(context.Background(), id) })
		if err := engine.StartContainer(ctx, id); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		facts, err := engine.InspectContainer(ctx, id)
		if err != nil || !facts.IdentityVerified || facts.HostIP == "" {
			t.Fatalf("inspect %s: %+v %v", name, facts, err)
		}
		return facts.HostIP
	}
	ipA := start("workos-p3-test-a", digestA, tarA)
	deadline := time.Now().Add(15 * time.Second)
	var bodyA string
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + ipA + ":8080/")
		if err == nil {
			buf := make([]byte, 64)
			n, _ := resp.Body.Read(buf)
			resp.Body.Close()
			bodyA = strings.TrimSpace(string(buf[:n]))
			if strings.Contains(bodyA, "WORKOS-P3-A") {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(bodyA, "WORKOS-P3-A") {
		t.Fatalf("expected A body, got %q", bodyA)
	}
	_ = engine.RemoveContainer(ctx, "workos-p3-test-a")
	ipB := start("workos-p3-test-b", digestB, tarB)
	var bodyB string
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + ipB + ":8080/")
		if err == nil {
			buf := make([]byte, 64)
			n, _ := resp.Body.Read(buf)
			resp.Body.Close()
			bodyB = strings.TrimSpace(string(buf[:n]))
			if strings.Contains(bodyB, "WORKOS-P3-B") {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(bodyB, "WORKOS-P3-B") || bodyA == bodyB {
		t.Fatalf("expected distinct A/B HTTP, A=%q B=%q", bodyA, bodyB)
	}
}
