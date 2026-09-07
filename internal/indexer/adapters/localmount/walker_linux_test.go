package localmount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"golang.org/x/sys/unix"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func requireLimit(t *testing.T, root string) {
	t.Helper()
	result, err := NewWalker().Walk(context.Background(), root)
	var failure *domain.WorkspaceFailure
	if !errors.As(err, &failure) || failure.Reason != domain.DegradedScanLimit || len(result.Files) != 0 {
		t.Fatalf("incomplete walk returned %d facts, error %v", len(result.Files), err)
	}
}
func TestWalkBounds(t *testing.T) {
	t.Run("sparse oversized file, FIFO and invalid text", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "huge.md", "")
		if err := os.Truncate(filepath.Join(root, "huge.md"), 1<<40); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(root, "pipe.md"), 0600); err != nil {
			t.Fatal(err)
		}
		write(t, root, "invalid.md", "\xff")
		write(t, root, "empty.md", "")
		write(t, root, "control.md", "\x1b")
		write(t, root, "late-nul.md", strings.Repeat("a", 9000)+"\x00")
		write(t, root, "valid.md", "bounded text")
		result, err := NewWalker().Walk(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Files) != 1 || result.Files[0].RelPath != "valid.md" || len(result.Skips) != 6 {
			t.Fatalf("result: %+v", result)
		}
	})
	t.Run("file count", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i <= domain.WorkspaceMaxFiles; i++ {
			write(t, root, fmt.Sprintf("%04d.md", i), "text")
		}
		requireLimit(t, root)
	})
	t.Run("total bytes", func(t *testing.T) {
		root := t.TempDir()
		content := strings.Repeat("a", domain.WorkspaceMaxFileBytes)
		for i := 0; i <= domain.WorkspaceMaxTotalBytes/domain.WorkspaceMaxFileBytes; i++ {
			write(t, root, fmt.Sprintf("%04d.md", i), content)
		}
		requireLimit(t, root)
	})
	t.Run("ignored entry budget", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i <= domain.WorkspaceMaxEntries; i++ {
			write(t, root, fmt.Sprintf("%05d.bin", i), "")
		}
		requireLimit(t, root)
	})
	t.Run("directory depth", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, strings.Repeat("dir/", domain.WorkspaceMaxDepth+1)), 0700); err != nil {
			t.Fatal(err)
		}
		requireLimit(t, root)
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewWalker().Walk(ctx, t.TempDir())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error: %v", err)
		}
	})
}

func TestWalkSymlinkReplacement(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "notes.md", "outside fixture must never be indexed")
	inner := filepath.Join(root, "docs")
	if err := os.Mkdir(inner, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, inner, "notes.md", "inside fixture")
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := NewWalker().ValidateRoot(link); !errors.Is(err, domain.ErrWorkspaceSymlinkEscape) {
		t.Fatalf("root symlink: %v", err)
	}
	// Swap an actual directory and an external symlink while both enumeration
	// and reads run. Pathname checks alone cannot preserve this boundary.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := unix.Renameat2(unix.AT_FDCWD, inner, unix.AT_FDCWD, link, unix.RENAME_EXCHANGE); err != nil {
				t.Errorf("exchange: %v", err)
				return
			}
		}
	})
	defer func() { close(stop); wg.Wait() }()
	successes := 0
	for i := 0; i < 200; i++ {
		result, err := NewWalker().Walk(context.Background(), root)
		if err != nil {
			if !errors.Is(err, domain.ErrWorkspaceDegraded) {
				t.Fatal(err)
			}
			continue
		}
		successes++
		for _, file := range result.Files {
			if string(file.Content) != "inside fixture" {
				t.Fatalf("outside content leaked from %s", file.RelPath)
			}
		}
	}
	t.Logf("%d complete scans; incomplete concurrent scans were rejected", successes)
}
