// Package transport serves the runtime-host admin socket (ADR-0033): the
// ONLY entry point for operator bundle imports. The socket is a local Unix
// domain file with 0600 permissions — filesystem access is the identity —
// and is never routed by the Gateway or reachable from browsers/App Bridges.
package transport

import (
	"context"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/bundleformat"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"connectrpc.com/connect"

	runtimev1 "github.com/yangtao121/workos/gen/go/workos/runtime/v1"
	"github.com/yangtao121/workos/gen/go/workos/runtime/v1/runtimev1connect"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/application"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

// artifactChunkBytes bounds one streaming chunk; the total is bounded by the
// service's encoded-bundle cap.
const artifactChunkBytes = 256 << 10

type AdminHandler struct{ service *application.Service }

// NewArtifactAdminHandler returns the Connect handler for the admin service.
func NewArtifactAdminHandler(service *application.Service) (string, http.Handler) {
	return runtimev1connect.NewArtifactAdminServiceHandler(
		&AdminHandler{service: service},
		connect.WithReadMaxBytes(artifactChunkBytes+(16<<10)),
	)
}

func (h *AdminHandler) ImportArtifact(ctx context.Context, stream *connect.ClientStream[runtimev1.ImportArtifactRequest]) (*connect.Response[runtimev1.ImportArtifactResponse], error) {
	if !stream.Receive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("import stream is empty"))
	}
	start := stream.Msg().GetStart()
	if start == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("first chunk must carry import metadata"))
	}
	reader := &chunkReader{stream: stream}
	result, err := h.service.Import(ctx, application.ImportRequest{
		OwnerUserID:    start.GetOwnerUserId(),
		AppID:          start.GetAppId(),
		IdempotencyKey: start.GetIdempotencyKey(),
		ExpectedDigest: start.GetExpectedDigest(),
		Reader:         reader,
	})
	if err != nil {
		return nil, adminError(err)
	}
	return connect.NewResponse(&runtimev1.ImportArtifactResponse{
		ArtifactId: result.Artifact.ID,
		Digest:     result.Artifact.Digest,
		SizeBytes:  result.Artifact.SizeBytes,
		FileCount:  result.Artifact.FileCount,
		Created:    result.Created,
		Origin:     result.Artifact.Origin,
	}), nil
}

// chunkReader adapts the client stream's data parts into one io.Reader. The
// constructor is called right after the start part was received; a second
// start part or a premature client abort surfaces as an error.
type chunkReader struct {
	stream  *connect.ClientStream[runtimev1.ImportArtifactRequest]
	started bool
	buf     []byte
	eof     bool
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 && !c.eof {
		if !c.stream.Receive() {
			if err := c.stream.Err(); err != nil {
				return 0, err
			}
			c.eof = true
			break
		}
		msg := c.stream.Msg()
		if msg.GetStart() != nil {
			return 0, errors.New("duplicate start chunk in import stream")
		}
		c.buf = msg.GetData()
		if len(c.buf) > artifactChunkBytes {
			return 0, bundleformat.ErrBundleTooLarge
		}
	}
	if len(c.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func adminError(err error) error {
	code := connect.CodeInternal
	sanitized := "artifact import failed"
	switch {
	case errors.Is(err, domain.ErrInvalidRequest),
		errors.Is(err, bundleformat.ErrBundleInvalid),
		errors.Is(err, bundleformat.ErrBundleTooLarge):
		code = connect.CodeInvalidArgument
		sanitized = "artifact bundle rejected: " + err.Error()
	case errors.Is(err, domain.ErrConflict):
		code = connect.CodeAlreadyExists
		sanitized = "artifact import conflicts with an existing bundle"
	case errors.Is(err, domain.ErrQuotaExceeded):
		code = connect.CodeResourceExhausted
		sanitized = "owner artifact quota exceeded"
	case errors.Is(err, domain.ErrUnavailable):
		code = connect.CodeFailedPrecondition
		sanitized = "artifact storage unavailable"
	case errors.Is(err, domain.ErrStoreUnavailable):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New(sanitized))
}

// ListenAdminSocket binds the runtime admin socket with the same staleness
// discipline as the gateway admin socket: probe before remove, 0600 mode,
// and never unlink a path another process owns.
func ListenAdminSocket(path string, handler http.Handler, logger *slog.Logger) (net.Listener, *http.Server, error) {
	if path == "" {
		return nil, nil, errors.New("runtime admin socket path is empty")
	}
	if stat, err := os.Lstat(path); err == nil {
		if stat.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("admin socket path %s exists and is not a Unix socket", path)
		}
		conn, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("admin socket %s is served by another process", path)
		} else if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, nil, fmt.Errorf("admin socket %s could not be proven stale: %w", path, dialErr)
		}
		bound, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			// The old owner removed its endpoint during the probe.
		} else if statErr != nil || !os.SameFile(stat, bound) || bound.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("admin socket %s changed while checking staleness", path)
		} else if err := os.Remove(path); err != nil {
			return nil, nil, fmt.Errorf("remove stale admin socket: %w", err)
		} else {
			logger.Info("removed stale runtime admin socket", "path", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inspect admin socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, fmt.Errorf("listen admin socket: %w", err)
	}
	if unix, ok := listener.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("chmod admin socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Streaming imports of up to ~132 MiB need a generous body window;
		// per-chunk and total bounds are enforced by the service.
		ReadTimeout: 10 * time.Minute,
		IdleTimeout: 60 * time.Second,
	}
	return listener, server, nil
}
