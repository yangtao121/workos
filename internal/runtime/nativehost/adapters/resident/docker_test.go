package resident

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

// Opt-in Docker inspect test catches daemon normalization that a synthetic
// childInspect fixture cannot. The image needs Node but the bridge is not
// started; create/inspect/remove only checks the hard container boundary.
func TestDockerProfileRealInspect(t *testing.T) {
	if os.Getenv("WORKOS_REAL_DOCKER_PROFILE_TEST") != "1" {
		t.Skip("set WORKOS_REAL_DOCKER_PROFILE_TEST=1 with a local Docker daemon")
	}
	image := os.Getenv("WORKOS_REAL_DOCKER_PROFILE_IMAGE")
	if image == "" {
		image = "workos-greenfield-runtime:p0"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e := New(Config{DockerSocket: "/var/run/docker.sock", Image: image,
		RenderDevice: "/dev/dri/renderD128", RenderGID: "107", GPUDriver: "nvidia"})
	session := domain.Session{SessionID: ids.UUIDv7{}.New(), OwnerUserID: ids.UUIDv7{}.New(), ProjectID: ids.UUIDv7{}.New(),
		Generation: 1, State: domain.StateQueued, Width: 800, Height: 600}
	spec := ports.ResidentLaunch{Session: session}
	dir := t.TempDir()
	id, err := e.docker.create(ctx, childName(session), e.childConfig(spec, dir))
	if err != nil {
		t.Fatalf("docker create: %v", err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := e.docker.remove(cleanup, id); err != nil {
			t.Errorf("remove profile probe: %v", err)
		}
	}()
	child, err := e.docker.inspect(ctx, id)
	if err != nil {
		t.Fatalf("docker inspect: %v", err)
	}
	imageID, err := e.docker.imageID(ctx, image)
	if err != nil {
		t.Fatalf("image inspect: %v", err)
	}
	if err := e.verifyChildProfile(spec, dir, imageID, child, false); err != nil {
		t.Fatalf("security profile mismatch: %v\nHostConfig=%+v\nConfig=%+v\nMounts=%+v", err, child.HostConfig, child.Config, child.Mounts)
	}
	withCredential := child
	withCredential.Config.Env = append(append([]string{}, child.Config.Env...), "WORKOS_DATABASE_URL=forbidden")
	if err := e.verifyChildProfile(spec, dir, imageID, withCredential, false); err == nil {
		t.Fatal("adoption accepted a database credential in child environment")
	}
	withNetwork := child
	withNetwork.HostConfig.NetworkMode = "host"
	if err := e.verifyChildProfile(spec, dir, imageID, withNetwork, false); err == nil {
		t.Fatal("adoption accepted host networking")
	}
	withSocket := child
	withSocket.HostConfig.Binds = append(append([]string{}, child.HostConfig.Binds...), "/var/run/docker.sock:/var/run/docker.sock:rw")
	if err := e.verifyChildProfile(spec, dir, imageID, withSocket, false); err == nil {
		t.Fatal("adoption accepted a Docker socket bind")
	}
	withHiddenMount := child
	withHiddenMount.Mounts = append(withHiddenMount.Mounts[:len(withHiddenMount.Mounts):len(withHiddenMount.Mounts)], child.Mounts[0])
	withHiddenMount.Mounts[1].Source = "/var/run/docker.sock"
	if err := e.verifyChildProfile(spec, dir, imageID, withHiddenMount, false); err == nil {
		t.Fatal("adoption accepted an extra mount to an allowed destination")
	}
}

func TestGPUProbeRealDocker(t *testing.T) {
	if os.Getenv("WORKOS_REAL_DOCKER_PROFILE_TEST") != "1" {
		t.Skip("set WORKOS_REAL_DOCKER_PROFILE_TEST=1 with a local Docker daemon")
	}
	image := os.Getenv("WORKOS_REAL_DOCKER_PROFILE_IMAGE")
	if image == "" {
		image = "workos-greenfield-runtime:p0"
	}
	ipcRoot := t.TempDir()
	if err := os.Chmod(ipcRoot, 0700); err != nil {
		t.Fatal(err)
	}
	e := New(Config{DockerSocket: "/var/run/docker.sock", Image: image, IPCRoot: ipcRoot,
		RenderDevice: "/dev/dri/renderD128", RenderGID: os.Getenv("WORKOS_REAL_RENDER_GID"), GPUDriver: "nvidia"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.baseAvailable(); err != nil {
		t.Fatalf("base availability: %v", err)
	}
	if _, err := e.docker.imageID(ctx, image); err != nil {
		t.Fatalf("image: %v", err)
	}
	if !e.docker.hasRuntime(ctx, "nvidia") {
		t.Fatal("daemon does not report NVIDIA runtime")
	}
	if err := e.probeGPU(ctx); err != nil {
		t.Fatalf("direct GPU child probe: %v", err)
	}
	if err := e.Available(ctx); err != nil {
		t.Fatalf("child GPU probe: %v", err)
	}
}
