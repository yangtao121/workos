// Preview HTTP accepts only a scoped capability in an opaque-origin frame.
package transport

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type ServingService interface {
	Request(context.Context, string, string, ports.Request) (ports.Response, error)
}
type PreviewServingHandler struct{ service ServingService }

func NewServingHandler(service ServingService, _ *slog.Logger) http.Handler {
	return &PreviewServingHandler{service}
}
func (h *PreviewServingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'; worker-src 'none'; sandbox allow-scripts allow-forms")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	// Module scripts and fetch from the opaque iframe need CORS. No cookies,
	// authorization header or device identity enter the development server.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
	default:
		http.Error(w, "method unavailable", 405)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/previews/")
	parts := strings.SplitN(rest, "/", 3)
	if !ok || len(parts) < 2 || !domain.ValidPreviewUUID(parts[0]) || !validToken(parts[1]) {
		http.NotFound(w, r)
		return
	}
	asset := ""
	if len(parts) == 3 {
		asset = parts[2]
	}
	decoded, err := url.PathUnescape(asset)
	if err != nil || len(decoded) > 4096 || strings.ContainsAny(decoded, "\\\x00") {
		http.NotFound(w, r)
		return
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == ".." || segment == "." {
			http.NotFound(w, r)
			return
		}
	}
	if len(r.URL.RawQuery) > 4096 {
		http.Error(w, "query too large", 413)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request too large", 413)
		return
	}
	headers := map[string]string{}
	for _, key := range []string{"Accept", "Content-Type", "If-None-Match"} {
		if value := r.Header.Get(key); len(value) <= 1024 {
			headers[key] = value
		}
	}
	response, err := h.service.Request(r.Context(), parts[0], parts[1], ports.Request{Method: r.Method, Path: "/" + decoded, Query: r.URL.RawQuery, Headers: headers, Body: body})
	if err != nil {
		http.Error(w, "preview unavailable", 404)
		return
	}
	for key, value := range response.Headers {
		if key == "Location" {
			location, err := url.Parse(value)
			if err != nil || location.IsAbs() || location.Host != "" || strings.HasPrefix(value, "//") {
				continue
			}
			if strings.HasPrefix(value, "/") {
				value = "/previews/" + parts[0] + "/" + parts[1] + value
			}
		}
		w.Header().Set(key, value)
	}
	w.WriteHeader(response.Status)
	if r.Method != "HEAD" {
		_, _ = w.Write(response.Body)
	}
}
func validToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
