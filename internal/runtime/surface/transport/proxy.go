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
	proxyBodyLimitBytes   = 8 << 20 // 8 MiB
	proxyHeaderBudget     = 64      // max response header count
	proxyHeaderLimitBytes = 64 << 10
	proxyTimeout          = 30 * time.Second
	proxyDrainLimitBytes  = 64 * 1024
)

// proxyClient pins the client the proxy uses: tight timeouts and no ambient
// credential transport. Redirects are handled explicitly by the round trip,
// never auto-followed.
func newProxyClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			// Do not let net/http add Accept-Encoding or transparently decode a
			// response behind the proxy's accounting. Encoded bodies are rejected
			// below, so the 8 MiB cap always measures what the browser receives.
			DisableCompression:     true,
			ResponseHeaderTimeout:  15 * time.Second,
			MaxIdleConns:           64,
			IdleConnTimeout:        60 * time.Second,
			MaxResponseHeaderBytes: proxyHeaderLimitBytes,
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
	"Content-Security-Policy", "Content-Security-Policy-Report-Only",
	"X-Frame-Options", "X-Content-Type-Options",
	"Referrer-Policy", "Cache-Control", "Strict-Transport-Security",
	"Permissions-Policy", "Permissions-Policy-Report-Only",
	"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
	"Cross-Origin-Resource-Policy",
	"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods", "Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers", "Access-Control-Max-Age",
	"Clear-Site-Data", "Alt-Svc", "Refresh", "Content-Disposition",
	"Report-To", "Reporting-Endpoints", "NEL", "Origin-Agent-Cluster",
	"Content-Encoding",
	// Redirects never pass through the filter: the backend cannot navigate
	// the surface anywhere. Only a rewrite that passes the strict path
	// grammar re-attaches a Location inside the owning session prefix.
	"Location",
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
	if !validProxyTarget(target) {
		t.logger.Warn("surface proxy target rejected")
		http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
		return
	}
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
		t.logger.Warn("surface proxy backend unreachable")
		http.Error(w, "surface unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if !filterResponseHeaders(w, response) {
		writeProxyFailure(w, http.StatusBadGateway)
		return
	}
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
	if response.ContentLength > proxyBodyLimitBytes {
		_, _ = io.CopyN(io.Discard, response.Body, proxyDrainLimitBytes)
		writeProxyFailure(w, http.StatusBadGateway)
		return
	}
	if r.Method == http.MethodHead {
		if response.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.CopyN(io.Discard, response.Body, proxyDrainLimitBytes)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, proxyBodyLimitBytes+1))
	if err != nil {
		t.logger.Warn("surface proxy read failed")
		writeProxyFailure(w, http.StatusBadGateway)
		return
	}
	if len(payload) > proxyBodyLimitBytes {
		t.logger.Warn("surface proxy body limit exceeded")
		writeProxyFailure(w, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(payload)
}

// filterResponseHeaders applies the strict response filter: hop-by-hop and
// security-softening headers are removed within the header budget, then the
// WorkOS security posture is set unconditionally.
func filterResponseHeaders(w http.ResponseWriter, response *http.Response) bool {
	// Encoded bodies are not forwarded: accepting one would make the proxy's
	// byte cap apply to compressed bytes while a browser expands an unbounded
	// representation. "identity" is semantically unencoded and is stripped.
	encodings := response.Header.Values("Content-Encoding")
	if len(encodings) > 1 || len(encodings) == 1 && !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity") {
		return false
	}
	connectionHeaders := make(map[string]struct{})
	for _, value := range response.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if name := strings.ToLower(strings.TrimSpace(token)); name != "" {
				connectionHeaders[name] = struct{}{}
			}
		}
	}
	count := 0
	bytes := 0
	filtered := make(http.Header)
	for name, values := range response.Header {
		valueCount := len(values)
		if valueCount == 0 {
			valueCount = 1
			bytes += len(name) + 4
		}
		count += valueCount
		for _, value := range values {
			// Each value is serialized as its own field line by net/http, so
			// repeated values pay the field-name overhead each time as well.
			bytes += len(name) + len(value) + 4
		}
		if count > proxyHeaderBudget || bytes > proxyHeaderLimitBytes {
			return false
		}
		if isStrippedResponseHeader(name) || isHopByHop(name) {
			continue
		}
		if _, nominated := connectionHeaders[strings.ToLower(name)]; nominated {
			continue
		}
		for _, value := range values {
			filtered.Add(name, value)
		}
	}
	for key := range w.Header() {
		w.Header().Del(key)
	}
	for name, values := range filtered {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	setAssetHeaders(w)
	return true
}

func validProxyTarget(target ports.ProxyTarget) bool {
	if !domain.ValidSessionUUID(target.SessionID) {
		return false
	}
	host, port, err := net.SplitHostPort(target.Endpoint)
	if err != nil || host != "127.0.0.1" || port == "" || port[0] == '0' {
		return false
	}
	for index := range port {
		if port[index] < '0' || port[index] > '9' {
			return false
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return false
	}
	if target.BackendPath == "/" {
		return true
	}
	if !strings.HasPrefix(target.BackendPath, "/") {
		return false
	}
	normalized, err := domain.NormalizeAssetPath(strings.TrimPrefix(target.BackendPath, "/"))
	return err == nil && target.BackendPath == "/"+normalized
}

func writeProxyFailure(w http.ResponseWriter, status int) {
	for key := range w.Header() {
		w.Header().Del(key)
	}
	setAssetHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	payload := []byte("surface backend response rejected\n")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
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
