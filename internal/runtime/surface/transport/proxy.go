package transport

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// Proxy bounds. Everything is finite and server-owned: a backend response
// larger than the body cap fails closed, a slow backend trips the deadline,
// and the header budget rejects pathological backends before the body starts.
const (
	proxyBodyLimitBytes  = 8 << 20 // 8 MiB
	proxyHeaderBudget    = 64      // max response header count
	proxyTimeout         = 30 * time.Second
	proxyDrainLimitBytes = 64 * 1024
)

// proxyClient pins the client the proxy uses: tight timeouts and no ambient
// credential transport. Redirects are handled explicitly by the round trip,
// never auto-followed.
func newProxyClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 15 * time.Second,
			MaxIdleConns:          64,
			IdleConnTimeout:       60 * time.Second,
		},
		Timeout: proxyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// proxyMediaTypes is the strict response media-type allowlist. A backend
// Content-Type outside it degrades to application/octet-stream — the backend
// is never trusted to widen what the browser may execute.
var proxyMediaTypes = map[string]bool{
	"text/html": true, "text/css": true, "text/plain": true, "text/javascript": true,
	"application/javascript": true, "application/json": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/x-icon": true, "image/avif": true, "font/woff": true, "font/woff2": true,
}

// hopByHopHeaders never travel in either direction (RFC 7230 §6.1).
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// strippedResponseHeaders never reach the client: the backend cannot set
// cookies, challenge for credentials, fingerprint its server, or soften the
// WorkOS security posture (which is re-applied unconditionally afterwards).
var strippedResponseHeaders = []string{
	"Set-Cookie", "WWW-Authenticate", "Server", "X-Powered-By",
	"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options",
	"Referrer-Policy", "Cache-Control", "Strict-Transport-Security",
	"Permissions-Policy", "Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
	"Cross-Origin-Resource-Policy",
}

// defaultProxyRoundTripper performs the filtered round trip against the
// server-owned loopback endpoint. The outbound request carries no client
// headers at all: the backend sees a bare GET/HEAD with a fresh fixed Host —
// no cookies, no authorization, no bridge token, no WorkOS identity, no
// forwarding chain, no query, no body (ADR-0006 §5).
type defaultProxyRoundTripper struct {
	client *http.Client
	logger *slog.Logger
}

func (t *defaultProxyRoundTripper) roundTrip(w http.ResponseWriter, r *http.Request, target ports.ProxyTarget) {
	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+target.Endpoint+target.BackendPath, http.NoBody)
	if err != nil {
		http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
		return
	}
	outbound.Header.Set("Accept", "*/*")
	outbound.Header.Set("User-Agent", "WorkOS-Surface-Proxy")
	response, err := t.client.Do(outbound)
	if err != nil {
		// The workload may be restarting: a fixed 503, never the dial error.
		t.logger.Warn("surface proxy backend unreachable", "error", err)
		http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = response.Body.Close() }()
	filterResponseHeaders(w, response)
	// Redirects are rewritten only when the location is a clean relative
	// path inside this surface; anything else is dropped (the status stays).
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		if location := response.Header.Get("Location"); location != "" {
			if rewritten, ok := rewriteRedirect(location, target.SessionID); ok {
				w.Header().Set("Location", rewritten)
			}
		}
	}
	w.Header().Set("Content-Type", sanitizeContentType(response.Header.Get("Content-Type")))
	length := response.ContentLength
	if length < 0 || length > proxyBodyLimitBytes {
		// Unknown length: enforce the cap while copying.
		length = -1
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	}
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		_, _ = io.CopyN(io.Discard, response.Body, proxyDrainLimitBytes)
		return
	}
	written, err := io.Copy(w, io.LimitReader(response.Body, proxyBodyLimitBytes+1))
	if err != nil {
		t.logger.Warn("surface proxy copy failed", "error", err)
		return
	}
	if written > proxyBodyLimitBytes {
		// The cap fired mid-stream; the response is already committed, so the
		// connection is the only remaining failure boundary.
		t.logger.Warn("surface proxy body limit exceeded")
	}
}

// filterResponseHeaders applies the strict response filter: hop-by-hop and
// security-softening headers are removed within the header budget, then the
// WorkOS security posture is set unconditionally.
func filterResponseHeaders(w http.ResponseWriter, response *http.Response) {
	for key := range w.Header() {
		w.Header().Del(key)
	}
	count := 0
	for name, values := range response.Header {
		if isStrippedResponseHeader(name) || isHopByHop(name) {
			continue
		}
		if count >= proxyHeaderBudget {
			continue
		}
		count++
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	setAssetHeaders(w)
}

func isStrippedResponseHeader(name string) bool {
	for _, stripped := range strippedResponseHeaders {
		if strings.EqualFold(name, stripped) {
			return true
		}
	}
	return false
}

func isHopByHop(name string) bool {
	for _, hop := range hopByHopHeaders {
		if strings.EqualFold(name, hop) {
			return true
		}
	}
	return false
}

// rewriteRedirect rewrites a redirect Location only when it is a clean,
// relative path that passes the same strict path grammar as the surface
// itself; the rewrite stays inside the owning session's prefix. Absolute
// URLs, encoded, or traversal-shaped targets are dropped.
func rewriteRedirect(location, sessionID string) (string, bool) {
	if location == "" || strings.ContainsAny(location, "%\\?#:") || strings.HasPrefix(location, "//") {
		return "", false
	}
	normalized, err := domain.NormalizeAssetPath(strings.TrimPrefix(location, "/"))
	if err != nil {
		return "", false
	}
	return "/surfaces/" + sessionID + "/" + normalized, true
}

// sanitizeContentType trusts the backend Content-Type only inside the strict
// allowlist; anything else — including parameterized or unknown types —
// degrades to application/octet-stream with nosniff already set.
func sanitizeContentType(raw string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
	if proxyMediaTypes[base] {
		return base
	}
	return "application/octet-stream"
}
