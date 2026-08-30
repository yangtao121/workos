package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/platform/config"
)

// AdminSocket is the Gateway-owned private operator edge: one Unix domain
// socket serving ONLY the DeviceAuthAdminService Connect handler, never on
// the public TCP listener and never in the proxy allowlist.
type AdminSocket struct {
	listener net.Listener
	server   *http.Server
	path     string
	fileInfo os.FileInfo
}

// ListenAdminSocket binds the configured path after re-verifying the local
// filesystem facts: an existing socket is only cleaned up once it is proven
// stale — nothing answers on it. The socket file is created with at most
// 0600 permissions.
func ListenAdminSocket(path string, adminHandler http.Handler, logger *slog.Logger) (*AdminSocket, error) {
	if err := config.ValidateAdminSocketPath(path); err != nil {
		return nil, err
	}
	if stat, err := os.Lstat(path); err == nil {
		if stat.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("admin socket path %s exists and is not a Unix socket", path)
		}
		// Probe before touching: a live listener proves another gateway
		// instance owns the socket, which is a hard startup failure, not a
		// cleanup case.
		if conn, dialErr := net.DialTimeout("unix", path, time.Second); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("admin socket %s is served by another process", path)
		} else if !provesStaleAdminSocket(dialErr) {
			// A timeout, permission denial, or any other ambiguous result does
			// not prove staleness and must preserve the existing endpoint.
			return nil, fmt.Errorf("admin socket %s could not be proven stale: %w", path, dialErr)
		}
		current, statErr := os.Lstat(path)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			// The old owner removed its endpoint after our probe; bind below.
		case statErr != nil:
			return nil, fmt.Errorf("recheck stale admin socket: %w", statErr)
		case current.Mode()&os.ModeSocket == 0 || !os.SameFile(stat, current):
			return nil, fmt.Errorf("admin socket %s changed while checking staleness", path)
		default:
			// Only the exact inode that refused a connection is removed — never
			// a replacement created during the probe.
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove stale admin socket: %w", err)
			}
			logger.Info("removed stale admin socket", "path", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect admin socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen admin socket: %w", err)
	}
	// Go's UnixListener otherwise unlinks the pathname unconditionally on
	// close. Disable that behavior so Close can compare the bound inode and
	// never remove a socket another process placed at the same path.
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("admin listener is not a Unix listener")
	}
	unixListener.SetUnlinkOnClose(false)
	// The validated parent is owned by this process's effective user and is
	// not writable by group/other, so the pathname created by Listen is the
	// ownership identity we retain for guarded cleanup. A Unix socket fd has
	// a kernel inode distinct from its filesystem node and cannot be compared
	// with Lstat using os.SameFile.
	boundInfo, err := os.Lstat(path)
	if err != nil || boundInfo.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		return nil, errors.New("bound admin socket path is unavailable")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		closeOwnedAdminSocket(listener, path, boundInfo)
		return nil, fmt.Errorf("chmod admin socket: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSocket == 0 || !os.SameFile(boundInfo, pathInfo) {
		closeOwnedAdminSocket(listener, path, boundInfo)
		return nil, errors.New("admin socket path changed while setting permissions")
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
	return &AdminSocket{listener: listener, server: server, path: path, fileInfo: boundInfo}, nil
}

func provesStaleAdminSocket(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func closeOwnedAdminSocket(listener net.Listener, path string, boundInfo os.FileInfo) {
	_ = listener.Close()
	if current, err := os.Lstat(path); err == nil && current.Mode()&os.ModeSocket != 0 &&
		boundInfo != nil && os.SameFile(boundInfo, current) {
		_ = os.Remove(path)
	}
}

// Serve runs the admin listener until Close. An unexpected listener failure
// is returned to the composition root, which terminates the whole Gateway.
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
	closeOwnedAdminSocket(a.listener, a.path, a.fileInfo)
	return err
}
