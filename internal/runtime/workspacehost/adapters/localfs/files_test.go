package localfs

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceAtomicWritesAndBoundaries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	files := &Files{}
	run := func(name string, args map[string]any) domain.Result {
		t.Helper()
		result, err := files.Execute(context.Background(), root, false, domain.Operation{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	created := run("fs.write", map[string]any{"path": "/workspace/file.txt", "content": "before", "guard": "createIfAbsent"})
	if created["operation"] != "create" {
		t.Fatal(created)
	}
	if result := run("fs.write", map[string]any{"path": "file.txt", "content": "bad", "guard": "createIfAbsent"}); result["error"] != "FS_NOT_OBSERVED" {
		t.Fatal(result)
	}
	if result := run("fs.edit", map[string]any{"path": "file.txt", "oldString": "before", "newString": "after", "version": "stale"}); result["error"] != "FS_STALE_VERSION" {
		t.Fatal(result)
	}
	changed := run("fs.edit", map[string]any{"path": "file.txt", "oldString": "before", "newString": "after", "version": created["version"]})
	if changed["after"] != "after" {
		t.Fatal(changed)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if result := run("fs.read", map[string]any{"path": "escape/secret"}); result["error"] != "FS_PERMISSION_DENIED" {
		t.Fatal(result)
	}
	if _, err := files.Execute(context.Background(), root, false, domain.Operation{Name: "fs.read", Arguments: map[string]any{"path": "../secret"}}); err == nil {
		t.Fatal("path traversal accepted")
	}
	if result, err := files.Execute(context.Background(), root, true, domain.Operation{Name: "fs.write", Arguments: map[string]any{"path": "file.txt", "content": "bad"}}); err != nil || result["error"] != "FS_PERMISSION_DENIED" {
		t.Fatalf("read-only=%v %v", result, err)
	}
}
