package dockerexec

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealWorkspaceContainerIsolation(t *testing.T) {
	base := os.Getenv("WORKOS_WORKSPACE_TEST_ROOT")
	if base == "" {
		t.Skip("set host-visible WORKOS_WORKSPACE_TEST_ROOT and expose Runtime Docker socket")
	}
	root, err := os.MkdirTemp(base, "workspace-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	outside := filepath.Join(base, "outside-marker")
	if err := os.WriteFile(outside, []byte("outside-secret-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outside)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "runtime-fixture-secret")
	engine := New("/var/run/docker.sock", "workos-workspace-runtime:dev")
	run := func(command string, ro bool, timeout time.Duration) domain.Result {
		t.Helper()
		result, err := engine.Execute(context.Background(), root, ro, domain.Operation{ID: (ids.UUIDv7{}).New(), Name: "shell.run", Arguments: map[string]any{"command": command, "timeoutMs": float64(timeout.Milliseconds())}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := run(`set -e; printf 'shared-workspace' > evidence.txt; cat evidence.txt; test ! -e /var/run/docker.sock; test ! -e /proc/1/root/home/aquatao; test -z "$DEEPSEEK_API_KEY"; ! cat escape; test ! -w /usr; node -e 'console.log("node-ready")'; go version`, false, 20*time.Second)
	output := result["stdout"].(map[string]any)["text"].(string)
	if result["exitCode"] != 0 || !strings.Contains(output, "shared-workspace") || !strings.Contains(output, "node-ready") || strings.Contains(output, "outside-secret-fixture") {
		t.Fatalf("isolation result: %v", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "evidence.txt"))
	if err != nil || string(data) != "shared-workspace" {
		t.Fatalf("shared tree: %s %v", data, err)
	}
	readonly := run(`printf forbidden > evidence.txt`, true, 10*time.Second)
	if readonly["exitCode"] == 0 {
		t.Fatal("read-only command changed workspace")
	}
	timed := run(`sleep 60`, false, 200*time.Millisecond)
	if timed["timedOut"] != true {
		t.Fatalf("deadline not enforced: %v", timed)
	}
}

func TestInvalidTimeoutNeverStartsAContainer(t *testing.T) {
	engine := New("/no-docker-socket", "fixture")
	for _, value := range []any{0.1, -1.0, math.NaN(), math.Inf(1), "100"} {
		_, err := engine.Execute(context.Background(), "/fixture", false, domain.Operation{Arguments: map[string]any{"command": "true", "timeoutMs": value}})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid timeout was not refused: %v", err)
		}
	}
}
