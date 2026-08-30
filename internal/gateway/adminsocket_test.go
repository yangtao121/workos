package gateway

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestListenAdminSocketRejectsLiveSocket pins the stale-socket contract: an
// existing socket that still answers is owned by another process and fails
// startup; only a proven-stale socket is removed.
func TestListenAdminSocketRejectsLiveSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-admin.sock")

	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	defer listener.Close()
	go func() {
		_ = http.Serve(listener, http.NotFoundHandler())
	}()

	logger := newTestLogger()
	handler := http.NotFoundHandler()
	if _, err := ListenAdminSocket(path, handler, logger); err == nil {
		t.Fatal("live admin socket was removed and rebound")
	}

	// Prove the live socket was not disturbed.
	if _, err := net.Dial("unix", path); err != nil {
		t.Fatalf("live socket was disturbed: %v", err)
	}
	_ = listener.Close()

	// After the owner stops, the socket is stale: binding must succeed.
	admin, err := ListenAdminSocket(path, handler, logger)
	if err != nil {
		t.Fatalf("stale admin socket not reclaimed: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admin.Close(shutdownCtx); err != nil {
		t.Fatalf("close reclaimed admin socket: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owned admin socket not removed on close: %v", err)
	}
}

func TestProvesStaleAdminSocketOnlyForConnectionRefused(t *testing.T) {
	t.Parallel()
	if !provesStaleAdminSocket(&net.OpError{Err: syscall.ECONNREFUSED}) {
		t.Fatal("connection refusal did not prove a stale socket")
	}
	for _, err := range []error{os.ErrPermission, context.DeadlineExceeded, syscall.ETIMEDOUT} {
		if provesStaleAdminSocket(&net.OpError{Err: err}) {
			t.Fatalf("ambiguous dial error %v was treated as stale", err)
		}
	}
}

func TestAdminSocketClosePreservesReplacementPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "gateway-admin.sock")
	admin, err := ListenAdminSocket(path, http.NotFoundHandler(), newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	replacement.(*net.UnixListener).SetUnlinkOnClose(false)
	defer func() {
		_ = replacement.Close()
		_ = os.Remove(path)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admin.Close(shutdownCtx); err != nil {
		t.Fatalf("close original admin socket: %v", err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("replacement socket was removed: %v", err)
	}
	_ = conn.Close()
}
