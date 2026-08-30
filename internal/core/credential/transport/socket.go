package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// MaxAdminSocketBodyBytes is the pre-decode budget the admin socket applies
// to every request before any handler observes it.
const MaxAdminSocketBodyBytes = MaxAdminRequestBytes

// AdminSocket is the Core-owned credential admin edge: one Unix domain
// socket serving ONLY the CredentialAdminService, never on the Core TCP
// listener, never proxied by the Gateway, and never reachable by
// harness-host. The lifecycle semantics mirror the Gateway's operator admin
// socket: an existing socket is removed only after it is proven stale, and
// only the exact inode this process bound is ever cleaned up.
type AdminSocket struct {
	listener net.Listener
	server   *http.Server
	path     string
	bound    os.FileInfo
}

// ListenAdminSocket validates the path grammar, creates the controlled
// runtime directory when absent, and binds the 0600 socket.
func ListenAdminSocket(path string, adminHandler http.Handler, logger *slog.Logger) (*AdminSocket, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("credential admin socket path must be absolute and cleaned")
	}
	if err := validateSocketParent(path); err != nil {
		return nil, err
	}
	if stat, err := os.Lstat(path); err == nil {
		if stat.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("credential admin socket path %s exists and is not a Unix socket", path)
		}
		if conn, dialErr := net.DialTimeout("unix", path, time.Second); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("credential admin socket %s is served by another process", path)
		} else if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			// A timeout, permission denial, or any other ambiguous result
			// does not prove staleness and must preserve the endpoint.
			return nil, fmt.Errorf("credential admin socket %s could not be proven stale: %w", path, dialErr)
		}
		current, statErr := os.Lstat(path)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			// The old owner removed its endpoint after the probe.
		case statErr != nil:
			return nil, fmt.Errorf("recheck stale credential admin socket: %w", statErr)
		case current.Mode()&os.ModeSocket == 0 || !os.SameFile(stat, current):
			return nil, fmt.Errorf("credential admin socket %s changed while checking staleness", path)
		default:
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("remove stale credential admin socket: %w", err)
			}
			logger.Info("removed stale credential admin socket", "path", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect credential admin socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen credential admin socket: %w", err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("credential admin listener is not a Unix listener")
	}
	unixListener.SetUnlinkOnClose(false)
	bound, err := os.Lstat(path)
	if err != nil || bound.Mode()&os.ModeSocket == 0 {
		closeOwnedSocket(listener, path, bound)
		return nil, errors.New("bound credential admin socket path is unavailable")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		closeOwnedSocket(listener, path, bound)
		return nil, fmt.Errorf("chmod credential admin socket: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSocket == 0 || !os.SameFile(bound, pathInfo) {
		closeOwnedSocket(listener, path, bound)
		return nil, errors.New("credential admin socket path changed while setting permissions")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			// Bounded before decoding: oversize admin requests never reach
			// the Connect handler or any application code.
			r.Body = http.MaxBytesReader(w, r.Body, MaxAdminSocketBodyBytes)
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
	return &AdminSocket{listener: listener, server: server, path: path, bound: bound}, nil
}

// validateSocketParent enforces the controlled-directory grammar: an
// absolute real non-symlink parent owned by this process's effective user
// and not writable by group or others. A missing parent one level deep is
// created with 0700 so container runtimes and systemd units can point the
// socket at a process-owned runtime directory.
func validateSocketParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if filepath.Clean(filepath.Dir(parent)) != "/" {
			return errors.New("credential admin socket parent must be a single controlled directory")
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			return fmt.Errorf("create credential admin socket directory: %w", err)
		}
		info, err = os.Lstat(parent)
	}
	if err != nil {
		return fmt.Errorf("credential admin socket parent is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential admin socket parent must be a real directory, not a symlink")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("credential admin socket parent must be owned by the core process user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("credential admin socket parent must not be writable by group or others")
	}
	return nil
}

func closeOwnedSocket(listener net.Listener, path string, bound os.FileInfo) {
	_ = listener.Close()
	if current, err := os.Lstat(path); err == nil && current.Mode()&os.ModeSocket != 0 &&
		bound != nil && os.SameFile(bound, current) {
		_ = os.Remove(path)
	}
}

// Serve runs the admin listener until Close. A runtime failure terminates
// the whole Core: an operator edge that dies silently is worse than no edge.
func (a *AdminSocket) Serve() error {
	err := a.server.Serve(a.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the admin listener and removes the exact bound socket.
func (a *AdminSocket) Close(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := a.server.Shutdown(shutdownCtx)
	closeOwnedSocket(a.listener, a.path, a.bound)
	return err
}
