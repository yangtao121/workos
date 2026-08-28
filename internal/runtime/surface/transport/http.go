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

// surfaceAssetPrefix is the single asset route this handler owns.
const surfaceAssetPrefix = "/surfaces/"

// AssetService is the serving surface the HTTP boundary needs: one bounded,
// revalidated asset read per request.
type AssetService interface {
	ServeAsset(ctx context.Context, ownerUserID, deviceID, sessionID, assetPath string) (ports.Asset, error)
}

// surfaceCSP is the fixed content security policy for every asset response.
// The bundle is untrusted user content: no connections, no object/frame
// embedding, no form targets, and framing only by the WorkOS origin itself.
// The sandbox directive is the server-enforced isolation boundary: every
// document served from this route — HTML entrypoint, SVG, or any other
// active-content type — runs in an opaque origin with scripts only, so the
// response is confined even when the URL is opened outside the Desktop
// iframe. It never grants same-origin, forms, popups, top navigation,
// downloads, or storage.
const surfaceCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'none'; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'self'; worker-src 'none'; " +
	"sandbox allow-scripts"

// AssetHandler serves one bounded, revalidated asset per request. Externally
// it speaks only 404/405/503 with fixed short messages; every internal error
// detail stays in the server log.
func NewAssetHandler(service AssetService, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAssetHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		asset, err := service.ServeAsset(r.Context(), id.UserID, id.DeviceID, sessionID, assetPath)
		if err != nil {
			switch {
			case errors.Is(err, domain.ErrUnavailable):
				http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
			default:
				// NotFound, invalid shapes, and internal invariant failures all
				// fail closed as 404 without leaking which one fired.
				logger.Warn("surface asset denied", "error", err, "method", r.Method)
				http.Error(w, "not found", http.StatusNotFound)
			}
			return
		}
		w.Header().Set("Content-Type", asset.MediaType)
		w.Header().Set("ETag", `"`+asset.Etag+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(asset.Content)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(asset.Content)
	})
}

// setAssetHeaders applies the fixed security headers to every response,
// including errors.
func setAssetHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", surfaceCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

// splitSurfacePath parses "/surfaces/<session-id>[/asset/path]". It uses the
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
