package dockerapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/runtime/workload/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeBundles struct{ path string }

func TestInspectCommandIncludesEntrypointArguments(t *testing.T) {
	var document inspectDocument
	document.Config.Entrypoint = []string{"/app/server"}
	document.Config.Cmd = []string{"unexpected-argument"}
	facts, err := factsFromInspect(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Command) != 2 || facts.Command[1] != "unexpected-argument" {
		t.Fatalf("Docker's executed argv was truncated: %+v", facts.Command)
	}
}

func (f fakeBundles) OpenForLaunch(context.Context, string, string) (string, error) {
	return f.path, nil
}

func TestStartConflictOnlyConvergesRemoval(t *testing.T) {
	for _, removal := range []bool{true, false} {
		message := "container is paused"
		if removal {
			message = "container is marked for removal and cannot be started"
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
		}))
		t.Cleanup(server.Close)
		engine := &Engine{client: server.Client(), baseURL: server.URL}
		err := engine.StartContainer(context.Background(), "owned")
		if err == nil || errors.Is(err, ports.ErrContainerRemoving) != removal {
			t.Fatalf("conflict %q classified as %v", message, err)
		}
	}
}

func TestProbeReportsDockerProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+apiVersion+"/info" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"CgroupVersion":"2","ServerVersion":"test"}`))
	}))
	t.Cleanup(server.Close)
	engine := &Engine{client: server.Client(), baseURL: server.URL + "/" + apiVersion, unpackRoot: t.TempDir(), bundles: fakeBundles{path: t.TempDir()}}
	capability, err := engine.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !capability.Available || capability.Rootless || !capability.AllowBridgeEndpoint || !capability.SkipMemoryHigh {
		t.Fatalf("docker profile must be available, not rootless, bridge, skip memory.high: %+v", capability)
	}
}

func TestImageExistsUsesLocalInspect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:" + strings.Repeat("1", 64), "RepoDigests": []string{"localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}})
	}))
	t.Cleanup(server.Close)
	engine := &Engine{client: server.Client(), baseURL: server.URL + "/" + apiVersion, unpackRoot: t.TempDir(), bundles: fakeBundles{}}
	exists, err := engine.ImageExists(context.Background(), "localhost/workos-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestFactsFromInspectRequireBundleIdentity(t *testing.T) {
	var document inspectDocument
	document.ID = "cid"
	document.Name = "/workos-app"
	document.Image = "sha256:ab"
	document.Config.Labels = map[string]string{
		"workos.artifact.digest": "sha256:cd",
		"workos.container.port":  "8080",
	}
	document.Config.Entrypoint = []string{"/app/server"}
	document.HostConfig.ReadonlyRootfs = true
	document.HostConfig.SecurityOpt = []string{"no-new-privileges:true"}
	document.HostConfig.NetworkMode = networkName
	document.NetworkSettings.Networks = map[string]struct {
		IPAddress string `json:"IPAddress"`
	}{networkName: {IPAddress: "172.18.0.2"}}
	document.Mounts = []struct {
		Type        string `json:"Type"`
		Destination string `json:"Destination"`
		Source      string `json:"Source"`
		RW          bool   `json:"RW"`
	}{{Type: "bind", Destination: "/app", Source: "/var/workos/unpack/gen", RW: false}}
	facts, err := factsFromInspect(document)
	if err != nil {
		t.Fatal(err)
	}
	if facts.HostIP != "172.18.0.2" || facts.PublishedPorts != 0 || facts.IdentityVerified || facts.ArtifactDigest == "" {
		t.Fatalf("docker inspect facts: %+v", facts)
	}
	document.NetworkSettings.Ports = map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}{"8080/tcp": {{HostIP: "127.0.0.1", HostPort: "1234"}}}
	facts, err = factsFromInspect(document)
	if err != nil {
		t.Fatal(err)
	}
	if facts.IdentityVerified {
		t.Fatal("published ports must fail identity verification")
	}
}

func TestConcurrentUnpackPublishesOnlyCompleteTree(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "server"), []byte(strings.Repeat("payload", 128*1024)), 0755); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "bundle")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := appbundle.EncodeDirectory(source, file)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{unpackRoot: t.TempDir(), bundles: fakeBundles{path: file.Name()}}
	start := make(chan struct{})
	outcomes := make(chan error, 12)
	var workers sync.WaitGroup
	for range 12 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			dest, err := engine.unpack(context.Background(), ports.ContainerSpec{Name: "workos-concurrent", ArtifactDigest: stats.Digest})
			if err == nil {
				actual, verifyErr := appbundle.EncodeDirectory(dest, io.Discard)
				err = verifyErr
				if err == nil && actual.Digest != stats.Digest {
					err = fmt.Errorf("partial tree published")
				}
			}
			outcomes <- err
		}()
	}
	close(start)
	workers.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
}
