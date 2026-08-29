package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// surfaceAssetPrefix is the single surface route this handler owns.
const surfaceAssetPrefix = "/surfaces/"

// SurfaceService is the serving surface the HTTP boundary needs: one
// bounded, revalidated content resolution per request, for either renderer.
type SurfaceService interface {
	ServeAsset(ctx context.Context, ownerUserID, deviceID, sessionID, assetPath string) (ports.Asset, error)
	ServeSurface(ctx context.Context, ownerUserID, deviceID, sessionID, rawPath string) (ports.SurfaceContent, error)
}

// surfaceCSP is the fixed content security policy for every surface response
// — assets and proxied documents alike. The content is untrusted user
// content: no connections, no object/frame embedding, no form targets, and
// framing only by the WorkOS origin itself. The sandbox directive is the
// server-enforced isolation boundary: every document served from this route
// runs in an opaque origin with scripts only, so the response is confined
// even when the URL is opened outside the Desktop iframe. It never grants
// same-origin, forms, popups, top navigation, downloads, or storage. A
// proxied backend cannot relax it: its own CSP and X-Frame-Options are
// stripped before this policy is set (ADR-0006 §5).
const surfaceCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'none'; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'self'; worker-src 'none'; " +
	"sandbox allow-scripts"

// SurfaceHandler serves one bounded, revalidated read per request across both
// renderers. Externally it speaks only 404/405/502/503 with fixed short
// messages; every internal error detail stays in the server log.
type SurfaceHandler struct {
	service SurfaceService
	proxy   proxyRoundTripper
	logger  *slog.Logger
}

type proxyRoundTripper interface {
	roundTrip(w http.ResponseWriter, r *http.Request, target ports.ProxyTarget)
}

func NewSurfaceHandler(service SurfaceService, logger *slog.Logger) *SurfaceHandler {
	return &SurfaceHandler{
		service: service,
		proxy:   &defaultProxyRoundTripper{client: newProxyClient(), logger: logger},
		logger:  logger,
	}
}

// NewAssetHandler keeps the historical constructor name for the web-bundle
// serving path.
func NewAssetHandler(service SurfaceService, logger *slog.Logger) http.Handler {
	return NewSurfaceHandler(service, logger)
}

func (h *SurfaceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setAssetHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" {
		h.logger.Warn("surface query denied", "method", r.Method)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id, err := identity.FromContext(r.Context())
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sessionID, assetPath, ok := splitSurfacePath(r.URL.EscapedPath())
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	content, err := h.service.ServeSurface(r.Context(), id.UserID, id.DeviceID, sessionID, assetPath)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrUnavailable):
			http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
		default:
			// NotFound, invalid shapes, and internal invariant failures all
			// fail closed as 404 without leaking which one fired.
			h.logger.Warn("surface request denied", "error", err, "method", r.Method)
			http.Error(w, "not found", http.StatusNotFound)
		}
		return
	}
	switch content.Kind {
	case ports.ContentAsset:
		h.serveAsset(w, r, content.Asset)
	case ports.ContentProxy:
		h.proxy.roundTrip(w, r, content.Proxy)
	default:
		h.logger.Warn("surface content kind invalid", "method", r.Method)
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *SurfaceHandler) serveAsset(w http.ResponseWriter, r *http.Request, asset ports.Asset) {
	w.Header().Set("Content-Type", asset.MediaType)
	w.Header().Set("ETag", `"`+asset.Etag+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(asset.Content)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(asset.Content)
}

// setAssetHeaders applies the fixed security headers to every response,
// including errors.
func setAssetHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", surfaceCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

// splitSurfacePath parses "/surfaces/<session-id>[/path]". It uses the
// escaped path only: any percent-encoded request is rejected before decoding
// so encoding ambiguity can never reach resolution.
func splitSurfacePath(path string) (sessionID, assetPath string, ok bool) {
	rest, found := strings.CutPrefix(path, surfaceAssetPrefix)
	if !found {
		return "", "", false
	}
	head, tail, _ := strings.Cut(rest, "/")
	if !domain.ValidSessionUUID(head) {
		return "", "", false
	}
	if strings.Contains(tail, "%") || strings.Contains(tail, "\\") {
		return "", "", false
	}
	return head, tail, true
}
