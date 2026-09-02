package transport

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func testSocketLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestIndexerAdminSocketRejectsLiveAndReclaimsProvenStale(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index-admin.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	go func() { _ = http.Serve(listener, http.NotFoundHandler()) }()
	if _, err := ListenAdminSocket(path, http.NotFoundHandler(), testSocketLogger()); err == nil {
		t.Fatal("live indexer admin socket was replaced")
	}
	if _, err := net.Dial("unix", path); err != nil {
		t.Fatalf("live socket was disturbed: %v", err)
	}
	_ = listener.Close()

	admin, err := ListenAdminSocket(path, http.NotFoundHandler(), testSocketLogger())
	if err != nil {
		t.Fatalf("stale socket was not reclaimed: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admin.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owned socket remains after close: %v", err)
	}
}

func TestIndexerAdminSocketRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	if _, err := ListenAdminSocket("relative.sock", http.NotFoundHandler(), testSocketLogger()); err == nil {
		t.Fatal("relative path was accepted")
	}
	root := t.TempDir()
	worldWritable := filepath.Join(root, "world")
	if err := os.Mkdir(worldWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worldWritable, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenAdminSocket(filepath.Join(worldWritable, "admin.sock"), http.NotFoundHandler(), testSocketLogger()); err == nil {
		t.Fatal("world-writable parent was accepted")
	}
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(root, "link")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenAdminSocket(filepath.Join(symlinkParent, "admin.sock"), http.NotFoundHandler(), testSocketLogger()); err == nil {
		t.Fatal("symlink parent was accepted")
	}
}

func TestIndexerAdminSocketOnlyTreatsRefusalAsStale(t *testing.T) {
	t.Parallel()
	if !provesStaleIndexerAdminSocket(&net.OpError{Err: syscall.ECONNREFUSED}) {
		t.Fatal("connection refusal did not prove staleness")
	}
	for _, err := range []error{os.ErrPermission, context.DeadlineExceeded, syscall.ETIMEDOUT} {
		if provesStaleIndexerAdminSocket(&net.OpError{Err: err}) {
			t.Fatalf("ambiguous error %v proved staleness", err)
		}
	}
}
