package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// AdminSocket is the Gateway-owned private operator edge: one Unix domain
// socket serving ONLY the DeviceAuthAdminService Connect handler, never on
// the public TCP listener and never in the proxy allowlist.
type AdminSocket struct {
	listener net.Listener
	server   *http.Server
	path     string
}

// ListenAdminSocket binds the configured path after re-verifying the local
// filesystem facts: only an exact stale socket may be cleaned up, and the
// socket file is created with at most 0600 permissions.
func ListenAdminSocket(path string, adminHandler http.Handler, logger *slog.Logger) (*AdminSocket, error) {
	if stat, err := os.Lstat(path); err == nil {
		if stat.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("admin socket path %s exists and is not a Unix socket", path)
		}
		// Only the exact configured stale socket is removed — never a
		// broader cleanup.
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale admin socket: %w", err)
		}
		logger.Info("removed stale admin socket", "path", path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen admin socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("chmod admin socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// The admin service is the only thing this socket serves; requests
		// are bounded before decoding.
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, adminMaxBodyBytes)
		}
		w.Header().Set("Cache-Control", "no-store")
		adminHandler.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return &AdminSocket{listener: listener, server: server, path: path}, nil
}

// Serve runs the admin listener until Close; the error is always a closure
// error after shutdown.
func (a *AdminSocket) Serve() error {
	err := a.server.Serve(a.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the admin listener and removes the exact socket file.
func (a *AdminSocket) Close(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := a.server.Shutdown(shutdownCtx)
	_ = a.listener.Close()
	if stat, statErr := os.Lstat(a.path); statErr == nil && stat.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(a.path)
	}
	return err
}
