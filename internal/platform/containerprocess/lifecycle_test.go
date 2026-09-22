package containerprocess

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
)

// This opt-in gate exercises the real Docker API. It never reconciles or lists
// another deployment's containers and always deletes only its own process.
func TestManualStopDockerLifetime(t *testing.T) {
	image := os.Getenv("WORKOS_LIFECYCLE_TEST_IMAGE")
	if image == "" {
		t.Skip("requires WORKOS_LIFECYCLE_TEST_IMAGE and Docker socket")
	}
	t.Setenv("WORKOS_RUNTIME_CONTAINER_NAMESPACE", "workos-lifecycle-test")
	client := New("/var/run/docker.sock", image)
	ctx, cancel := context.WithCancel(context.Background())
	process, err := client.Start(ctx, Spec{ID: ids.UUIDv7{}.New(), Argv: []string{"/bin/sh", "-c", "sleep 3600"}, ManualStop: true})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Stop()
	cancel() // Admitting request cancellation must never kill the program.
	var inspected struct {
		Config     struct{ Cmd []string }
		HostConfig struct {
			NetworkMode                 string
			ReadonlyRootfs              bool
			PidsLimit, Memory, NanoCpus int64
		}
		State struct{ Running bool }
	}
	if err := client.request(context.Background(), "GET", "/containers/"+process.ID+"/json", nil, &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected.Config.Cmd) != 3 || inspected.Config.Cmd[0] != "/bin/sh" {
		t.Fatalf("manual-stop command wrapped in timer: %#v", inspected.Config.Cmd)
	}
	if inspected.HostConfig.NetworkMode != "none" || !inspected.HostConfig.ReadonlyRootfs || inspected.HostConfig.PidsLimit != 128 || inspected.HostConfig.Memory != 1<<30 || inspected.HostConfig.NanoCpus != 2_000_000_000 {
		t.Fatal("resource/isolation bounds changed")
	}
	duration := time.Second
	if value := os.Getenv("WORKOS_LIFECYCLE_TEST_DURATION"); value != "" {
		duration, err = time.ParseDuration(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("manual-stop process %s: inspecting continued liveness after %s", process.ID, duration)
	select {
	case <-process.Done():
		t.Fatal("manual-stop program exited before explicit stop")
	case <-time.After(duration):
	}
	if err := client.request(context.Background(), "GET", "/containers/"+process.ID+"/json", nil, &inspected); err != nil || !inspected.State.Running {
		t.Fatalf("process is not running after %s: %v", duration, err)
	}
	process.Stop()
	select {
	case <-process.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not reap process")
	}
	t.Logf("manual-stop stayed alive for %s, then explicit stop reaped it", duration)
	bounded, err := client.Start(context.Background(), Spec{ID: ids.UUIDv7{}.New(), Argv: []string{"/bin/sh", "-c", "sleep 60"}, Lifetime: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer bounded.Stop()
	select {
	case <-bounded.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("bounded program failed to expire")
	}
}
