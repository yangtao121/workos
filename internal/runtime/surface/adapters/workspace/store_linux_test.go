package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

var scope = ports.FileScope{OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc352", ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc353"}

func mounted(t *testing.T, root string, readOnly bool) *Store {
	t.Helper()
	s, err := New([]Mount{{OwnerUserID: scope.OwnerUserID, ProjectID: scope.ProjectID, Path: root, ReadOnly: readOnly}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func TestWorkspaceWriteReadAndRestart(t *testing.T) {
	root := t.TempDir()
	s := mounted(t, root, false)
	ctx := context.Background()
	ref, err := s.Write(ctx, scope, domain.FileRef{ProjectID: scope.ProjectID, Path: "notes.txt"}, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.Read(ctx, scope, ref)
	if err != nil || string(data) != "first" {
		t.Fatalf("read %q: %v", data, err)
	}
	other := mounted(t, root, false)
	next, err := other.Write(ctx, scope, ref, []byte("second"))
	if err != nil || next.ETag == ref.ETag {
		t.Fatal(err)
	}
	if _, err = s.Read(ctx, scope, ref); !errors.Is(err, domain.ErrFileConflict) {
		t.Fatalf("stale read: %v", err)
	}
	if _, err = s.Write(ctx, scope, ref, []byte("lost")); !errors.Is(err, domain.ErrFileConflict) {
		t.Fatalf("stale write: %v", err)
	}
	data, err = s.Read(ctx, scope, next)
	if err != nil || string(data) != "second" {
		t.Fatalf("preserved %q: %v", data, err)
	}
	if _, err = s.Write(ctx, scope, next, make([]byte, domain.MaxFileBytes+1)); !errors.Is(err, domain.ErrFileLimit) {
		t.Fatal(err)
	}
	if _, err = mounted(t, root, true).Write(ctx, scope, next, nil); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatal(err)
	}
}
func TestWorkspaceConcurrentWritersOnlyOneWins(t *testing.T) {
	root := t.TempDir()
	s := mounted(t, root, false)
	other := mounted(t, root, false)
	ctx := context.Background()
	ref, err := s.Write(ctx, scope, domain.FileRef{ProjectID: scope.ProjectID, Path: "notes.txt"}, []byte("initial"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{s, other} {
		wg.Go(func() { _, err := store.Write(ctx, scope, ref, []byte("new")); results <- err })
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrFileConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
func TestWorkspaceRejectsPathsLinksAndForeignScope(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	s := mounted(t, root, false)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "dir"} {
		if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"../secret", "/secret", ".env", "dir/../secret", "dir/secret", "link/secret", "notes\\secret"} {
		_, err := s.Write(ctx, scope, domain.FileRef{ProjectID: scope.ProjectID, Path: name}, nil)
		if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := s.Write(ctx, scope, domain.FileRef{ProjectID: scope.OwnerUserID, Path: "file"}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
	foreign := scope
	foreign.OwnerUserID = scope.ProjectID
	if _, err := s.List(ctx, foreign, "", ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(data) != "outside" {
		t.Fatal("outside content changed")
	}
	page, err := s.List(ctx, scope, "", "")
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("links listed: %+v %v", page, err)
	}
	if _, err := New([]Mount{{OwnerUserID: scope.OwnerUserID, ProjectID: scope.ProjectID, Path: filepath.Join(root, "link")}}); err == nil {
		t.Fatal("symlink mount accepted")
	}
}
func TestWorkspaceDirectoryPaginationAndBounds(t *testing.T) {
	root := t.TempDir()
	s := mounted(t, root, false)
	ctx := context.Background()
	for i := range 21 {
		name := string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "huge"), []byte(strings.Repeat("x", domain.MaxFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := s.List(ctx, scope, "", "")
	if err != nil || len(first.Entries) != 20 || first.NextAfter != "t.txt" {
		t.Fatalf("first %+v %v", first, err)
	}
	last, err := s.List(ctx, scope, "", first.NextAfter)
	if err != nil || len(last.Entries) != 1 || last.NextAfter != "" {
		t.Fatalf("last %+v %v", last, err)
	}
	for _, entry := range first.Entries {
		if _, err := s.Read(ctx, scope, entry.Ref); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceClearsItsOrphanedWriteAndBoundsCreation(t *testing.T) {
	root := t.TempDir()
	s := mounted(t, root, false)
	ctx := context.Background()
	orphan := ".workos-write-0198d7ea-2110-7c42-b659-c5e4d73bc399"
	if err := os.WriteFile(filepath.Join(root, orphan), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := s.Write(ctx, scope, domain.FileRef{ProjectID: scope.ProjectID, Path: "notes.txt"}, []byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, orphan)); !os.IsNotExist(err) {
		t.Fatal("orphan was not cleared")
	}
	for i := range domain.MaxDirectoryEntries - 1 {
		name := fmt.Sprintf("file-%04d", i)
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Write(ctx, scope, domain.FileRef{ProjectID: scope.ProjectID, Path: "extra.txt"}, nil); !errors.Is(err, domain.ErrFileLimit) {
		t.Fatal(err)
	}
	if _, err = s.Write(ctx, scope, ref, []byte("updated")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePreservesExecutableMode(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "build.sh")
	if err := os.WriteFile(name, []byte("before"), 0700); err != nil {
		t.Fatal(err)
	}
	s := mounted(t, root, false)
	_, err := s.Write(context.Background(), scope, domain.FileRef{ProjectID: scope.ProjectID, Path: "build.sh", ETag: digest([]byte("before"))}, []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("mode was not preserved: %v", err)
	}
}
