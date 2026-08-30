package gateway

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"
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
	if _, err := ListenAdminSocket(path, handler, logger); err != nil {
		t.Fatalf("stale admin socket not reclaimed: %v", err)
	}
}
