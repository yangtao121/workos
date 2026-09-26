package resident

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

func TestNativeChildApplicationIsPinnedToSession(t *testing.T) {
	code := domain.Session{SessionID: ids.UUIDv7{}.New(), OwnerUserID: ids.UUIDv7{}.New(), ProjectID: ids.UUIDv7{}.New(), Generation: 1, State: domain.StateQueued, Width: 800, Height: 600, Application: domain.ApplicationCode}
	if _, ok := childLabels(code)["workos.application"]; ok {
		t.Fatal("legacy Code child profile changed")
	}
	codeArgs := childArgs(ports.ResidentLaunch{Session: code}, "/dev/dri/renderD128")
	editor := code
	editor.Application = domain.ApplicationTextEditor
	editorArgs := childArgs(ports.ResidentLaunch{Session: editor}, "/dev/dri/renderD128")
	if !reflect.DeepEqual(editorArgs, append(append([]string{}, codeArgs...), "--application", "text_editor")) {
		t.Fatalf("editor child arguments do not pin the app: %v", editorArgs)
	}
	if childLabels(editor)["workos.application"] != "text_editor" || !validLaunch(ports.ResidentLaunch{Session: editor}) {
		t.Fatal("editor child identity was not accepted")
	}
	editor.Application = "unknown"
	if validLaunch(ports.ResidentLaunch{Session: editor}) {
		t.Fatal("unknown child application accepted")
	}
}

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

func TestReapResidentExactIDWithoutWorkspaceGrant(t *testing.T) {
	shared := os.Getenv("WORKOS_REAL_SHARED_ROOT")
	if os.Getenv("WORKOS_REAL_DOCKER_REAP_TEST") != "1" || shared == "" {
		t.Skip("set WORKOS_REAL_DOCKER_REAP_TEST=1 and a same-path Docker shared root")
	}
	image := os.Getenv("WORKOS_REAL_DOCKER_PROFILE_IMAGE")
	if image == "" {
		image = "workos-greenfield-runtime:p0"
	}
	root, err := os.MkdirTemp(shared, "resident-reap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	e := New(Config{DockerSocket: "/var/run/docker.sock", Image: image, IPCRoot: root,
		RenderDevice: "/dev/dri/renderD128", RenderGID: os.Getenv("WORKOS_REAL_RENDER_GID"), GPUDriver: "nvidia"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := domain.Session{SessionID: ids.UUIDv7{}.New(), OwnerUserID: ids.UUIDv7{}.New(), ProjectID: ids.UUIDv7{}.New(),
		Generation: 3, State: domain.StateRunning, Width: 800, Height: 600}
	grant := ports.WorkspaceGrant{Directory: workspace, ReadOnly: true}
	ipc := cleanIPCDir(root, session)
	if err := os.Mkdir(ipc, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := e.docker.create(ctx, childName(session), e.childConfig(ports.ResidentLaunch{Session: session, Workspace: grant}, ipc))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = e.docker.remove(cleanup, id)
	}()
	imageID, err := e.docker.imageID(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	session.ChildContainerID, session.ChildImageID, session.ChildGeneration = id, imageID, session.Generation
	wrongOwner := session
	wrongOwner.OwnerUserID = ids.UUIDv7{}.New()
	if err := e.ReapResident(ctx, ports.ResidentLaunch{Session: wrongOwner}); err == nil {
		t.Fatal("foreign owner deleted pinned child")
	}
	if _, err := e.docker.inspect(ctx, id); err != nil {
		t.Fatalf("foreign-owner refusal removed child: %v", err)
	}
	if err := e.ReapResident(ctx, ports.ResidentLaunch{Session: session}); err != nil {
		t.Fatalf("pinned-ID cleanup without Core workspace grant: %v", err)
	}
	if _, err := e.docker.inspect(ctx, id); !errors.Is(err, domain.ErrResidentChildNotFound) {
		t.Fatalf("exact pinned child still exists: %v", err)
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
