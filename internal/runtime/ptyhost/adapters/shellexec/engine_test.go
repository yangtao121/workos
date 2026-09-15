package shellexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLaunchWorkingDirectoryStartsShellInWorkspace proves the PTY engine
// binds the operator-resolved working directory: a real login-shell child
// reports the directory itself.
func TestLaunchWorkingDirectoryStartsShellInWorkspace(t *testing.T) {
	workspace, err := os.MkdirTemp("", "pty-workspace-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(workspace)
	engine, err := New("")
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	terminal, err := engine.Launch(context.Background(), 80, 24, workspace)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() {
		terminal.Stop()
		time.Sleep(50 * time.Millisecond)
	}()
	probe := "echo PWD_IS:$PWD\r"
	if err := terminal.Write(context.Background(), []byte(probe)); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var output []byte
	var cursor int64
	for time.Now().Before(deadline) {
		next, chunk, err := terminal.Read(context.Background(), cursor, 16*1024)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		output = append(output, chunk...)
		cursor = next
		if strings.Contains(string(output), "PWD_IS:"+workspace) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("shell did not report the workspace directory; output: %q", string(output))
}

// TestLaunchRejectsMissingWorkingDirectory proves a vanished workspace
// fails admission instead of silently falling back elsewhere.
func TestLaunchRejectsMissingWorkingDirectory(t *testing.T) {
	engine, err := New("")
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if _, err := engine.Launch(context.Background(), 80, 24, filepath.Join(os.TempDir(), "pty-missing-workspace-does-not-exist")); err == nil {
		t.Fatal("launch accepted a missing working directory")
	}
}
