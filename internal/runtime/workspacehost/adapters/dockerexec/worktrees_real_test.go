package dockerexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

func TestDelegatedWorktreesAgainstDocker(t *testing.T) {
	root := os.Getenv("WORKOS_WORKTREE_PROBE_ROOT")
	if root == "" {
		t.Skip("set WORKOS_WORKTREE_PROBE_ROOT to a shared host/container path")
	}
	dir, err := os.MkdirTemp(root, "isolated-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "project")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	gen := ids.UUIDv7{}
	engine := New("/var/run/docker.sock", "workos-workspace-runtime:dev")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := func(root, git, command string) domain.Result {
		t.Helper()
		result, err := engine.Execute(ctx, root, false, domain.Operation{ID: gen.New(), GitDirectory: git, Arguments: map[string]any{"command": command}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := successfulOutput(result); !ok {
			t.Fatalf("fixture command failed: %+v", result)
		}
		return result
	}
	run(source, "", "git init -q && git config user.name Fixture && git config user.email fixture@example.invalid && printf 'parent\\n' > shared.txt && git add shared.txt && git commit -qm baseline")
	// Untrusted project config is not copied or executed during preparation.
	run(source, "", "git config core.fsmonitor 'touch /source/escaped-hook; false' && git config core.hooksPath /source/hooks")
	worktrees := NewWorktrees(engine, filepath.Join(dir, "delegations"))
	operations := []domain.Operation{{ID: gen.New(), DelegationID: gen.New()}, {ID: gen.New(), DelegationID: gen.New()}}
	trees := make([]ports.Worktree, 2)
	failures := make([]error, 2)
	var wg sync.WaitGroup
	for i := range trees {
		wg.Add(1)
		go func() { defer wg.Done(); trees[i], failures[i] = worktrees.Prepare(ctx, source, operations[i]) }()
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if trees[0].BaseCommit != trees[1].BaseCommit {
		t.Fatal("children did not share committed baseline")
	}
	if _, err := os.Stat(filepath.Join(source, "escaped-hook")); !os.IsNotExist(err) {
		t.Fatal("project fsmonitor executed")
	}
	run(trees[0].Root, trees[0].GitDirectory, "printf 'first\\n' > shared.txt && printf 'new-file\\n' > added.txt && test $(git rev-parse --git-common-dir) = /git")
	run(trees[1].Root, trees[1].GitDirectory, "test $(cat shared.txt) = parent && printf 'second\\n' > sibling.txt && test ! -e /source")
	parent, err := os.ReadFile(filepath.Join(source, "shared.txt"))
	if err != nil || string(parent) != "parent\n" {
		t.Fatal("child modified parent")
	}
	if _, err := os.Stat(filepath.Join(trees[1].Root, "added.txt")); !os.IsNotExist(err) {
		t.Fatal("child modified sibling")
	}
	diff, err := worktrees.Diff(ctx, trees[0], domain.Operation{ID: gen.New(), DelegationID: operations[0].DelegationID})
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := diff["diff"].(string)
	if !strings.Contains(patch, "+first") || !strings.Contains(patch, "+new-file") {
		t.Fatalf("diff omitted child output: %q", patch)
	}
	// Dirty source is explicitly refused, and a duplicate prepare cannot erase
	// either a committed receipt's files or a partially prepared directory.
	if err := os.WriteFile(filepath.Join(source, "shared.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worktrees.Prepare(ctx, source, domain.Operation{ID: gen.New(), DelegationID: gen.New()}); err == nil {
		t.Fatal("dirty parent silently cloned")
	}
	if _, err := worktrees.Prepare(ctx, source, operations[0]); err == nil {
		t.Fatal("existing tree recreated")
	}
}
