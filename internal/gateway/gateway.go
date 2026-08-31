package gateway

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	authtransport "github.com/yangtao121/workos/internal/gateway/auth/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// Body budgets (ADR-0007): authentication requests decode at most 16 KiB,
// admin socket requests at most 4 KiB. Business Connect routes keep their
// Core-side bounded decode.
const (
	authMaxBodyBytes  = 16 * 1024
	adminMaxBodyBytes = 4 * 1024
)

// RateLimiterBounds: per-remote-IP and process-global fixed-window budgets
// for the anonymous auth endpoints. The hard remote map capacity prevents
// attacker-chosen addresses from growing memory without bound.
const (
	AuthRemoteRateLimit = 60
	AuthGlobalRateLimit = 600
	AuthRateWindow      = time.Minute
	AuthRateMaxKeys     = 4096
)

// AuthStack carries the production device-auth wiring. It is nil in the
// development-bypass mode.
type AuthStack struct {
	Service *application.Service
	// Pairing and Device are the Connect transports of the Gateway-local
	// services, built by the composition root from the same service.
	Pairing http.Handler
	Device  http.Handler
	// RemoteLimiter and GlobalLimiter jointly bound the anonymous auth
	// endpoints. Both are mandatory in production so address rotation can
	// never bypass the process-wide budget.
	RemoteLimiter *application.RateLimiter
	GlobalLimiter *application.RateLimiter
}

// Handler routes the public edge: allowlisted public Connect services and the
// Desktop SPA to Core, the public SurfaceService and /surfaces/ assets to the
// Runtime upstream, and — when configured — the public IncidentService to
// the Reliability upstream. Every proxied path passes the same device-session
// gate and receives trusted identity headers derived from the validated
// session (or from configuration in development-bypass mode); spoofed inbound
// copies are dropped. In production the Gateway additionally serves its own
// DevicePairingService (anonymous) and DeviceService (session-authenticated)
// and validates request Host/Origin against the configured public origin.
type Handler struct {
	config      config.Config
	proxy       *httputil.ReverseProxy
	runtime     *httputil.ReverseProxy
	reliability *httputil.ReverseProxy
	logger      *slog.Logger
	auth        *AuthStack
	// originHost is the exact Host value requests must carry in production.
	originHost string
	// pairingPath/devicePath are the Connect prefixes served locally.
	pairingPath string
	devicePath  string
}

var publicServicePrefixes = []string{
	"/workos.agent.v1.AgentApprovalService/",
	"/workos.agent.v1.AgentAppPolicyService/",
	"/workos.agent.v1.AgentTaskService/",
	"/workos.agent.v1.AgentAppUsageService/",
	"/workos.app.v1.AppInstallationService/",
	"/workos.app.v1.AppRegistryService/",
	"/workos.artifact.v1.ArtifactService/",
	"/workos.common.v1.SystemService/",
	"/workos.harness.v1.HarnessCatalogService/",
	"/workos.project.v1.ProjectHarnessBindingService/",
	"/workos.project.v1.ProjectService/",
}

// runtimeServicePrefixes are the only public services routed to the Runtime
// upstream. Workload and private host management stay unreachable here.
var runtimeServicePrefixes = []string{
	"/workos.surface.v1.SurfaceService/",
	"/workos.bridge.v1.AppBridgeService/",
}

// incidentServicePrefix is the only public service routed to the optional
// Reliability upstream. It is added to the dispatch only when the upstream
// is configured; the rest of the gateway (readiness included) never depends
// on it, and the bridge credential stays stripped on the route.
const incidentServicePrefix = "/workos.incident.v1.IncidentService/"

// surfaceAssetPrefix is the public, same-origin surface asset route.
const surfaceAssetPrefix = "/surfaces/"

func New(cfg config.Config, logger *slog.Logger, auth *AuthStack) (*Handler, error) {
	core, err := newUpstreamProxy(cfg.Services.Core, cfg, logger, "core")
	if err != nil {
		return nil, err
	}
	runtime, err := newUpstreamProxy(cfg.Services.Runtime, cfg, logger, "runtime")
	if err != nil {
		return nil, err
	}
	// The Reliability upstream is optional: an unconfigured URL simply keeps
	// the incident routes 404, and nothing else — readiness included —
	// depends on it.
	var reliability *httputil.ReverseProxy
	if strings.TrimSpace(cfg.Services.Reliability) != "" {
		reliability, err = newUpstreamProxy(cfg.Services.Reliability, cfg, logger, "reliability")
		if err != nil {
			return nil, err
		}
	}
	handler := &Handler{
		config: cfg, proxy: core, runtime: runtime, reliability: reliability,
		logger: logger, auth: auth,
	}
	if !cfg.Auth.DevBypass {
		// Production mode requires the auth stack: the constructor fails
		// instead of serving a gate that cannot resolve sessions.
		if auth == nil || auth.Service == nil || auth.Pairing == nil || auth.Device == nil ||
			auth.RemoteLimiter == nil || auth.GlobalLimiter == nil {
			return nil, errors.New("production auth requires the device auth stack")
		}
		origin, err := url.Parse(cfg.Auth.PublicOrigin)
		if err != nil || origin.Host == "" {
			return nil, errors.New("public origin must be a canonical https origin")
		}
		handler.originHost = origin.Host
		handler.pairingPath = "/workos.auth.v1.DevicePairingService/"
		handler.devicePath = "/workos.auth.v1.DeviceService/"
	}
	return handler, nil
}

// newUpstreamProxy builds one reverse proxy whose director always drops
// client-supplied identity headers and re-injects the trusted owner/device
// pair resolved from the per-request session, so spoofing the inbound
// request can never reach an upstream. The bridge token header is scoped to
// the Runtime Connect routes only: the Core director strips it, so the
// credential can never travel to Core services or the Desktop SPA static
// handler, and nothing logs it. The constructor rejects targets that could
// never work — relative paths, scheme-less or unsupported-scheme strings,
// empty hosts — instead of deferring the failure to the first request, even
// when composition-root validation was bypassed.
func newUpstreamProxy(target string, cfg config.Config, logger *slog.Logger, name string) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(target)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid %s URL: must be an absolute http(s) URL with a host", name)
	}
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		// Identity never comes from the inbound request. The gate resolved
		// it from the validated session (or from configuration under the
		// loopback development bypass) and stored it in the context; a
		// missing identity can never be proxied. Proxy-supplied forwarding
		// chains are equally client-controlled and never travel upstream:
		// the Gateway is the TLS terminator and the origin of truth.
		request.Header.Del(identity.UserHeader)
		request.Header.Del(identity.DeviceHeader)
		request.Header.Del("Forwarded")
		for name := range request.Header {
			if len(name) >= 12 && strings.EqualFold(name[:12], "X-Forwarded-") {
				request.Header.Del(name)
			}
		}
		if id, err := identity.FromContext(request.Context()); err == nil {
			request.Header.Set(identity.UserHeader, id.UserID)
			request.Header.Set(identity.DeviceHeader, id.DeviceID)
		}
		// Credentials, sessions, and proof material never travel upstream:
		// the client Cookie header is dropped for every proxied request, and
		// the bridge credential survives only on the AppBridge Connect
		// routes.
		request.Header.Del("Cookie")
		bridgeToken := request.Header.Get(identity.BridgeTokenHeader)
		request.Header.Del(identity.BridgeTokenHeader)
		if appBridgeConnectPath(request.URL.Path) && bridgeToken != "" {
			request.Header.Set(identity.BridgeTokenHeader, bridgeToken)
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		logger.Error(name+" proxy failed", "error", err)
		http.Error(w, "workos "+name+" unavailable", http.StatusServiceUnavailable)
	}
	return proxy, nil
}

// appBridgeConnectPath reports whether the request targets the public
// AppBridge Connect service — the only route allowed to carry the bridge
// credential to an upstream.
func appBridgeConnectPath(path string) bool {
	return strings.HasPrefix(path, "/workos.bridge.v1.AppBridgeService/")
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.config.Auth.DevBypass {
		h.serveDev(w, r)
		return
	}
	h.serveProduction(w, r)
}

// serveDev keeps the loopback development behavior: the fixed configured
// identity is injected into the context and every public path proxies.
func (h *Handler) serveDev(w http.ResponseWriter, r *http.Request) {
	if publicConnectPath(r.URL.Path) {
		h.gate(w, r, h.proxy)
		return
	}
	if runtimeConnectPath(r.URL.Path) || strings.HasPrefix(r.URL.Path, surfaceAssetPrefix) {
		h.gate(w, r, h.runtime)
		return
	}
	if strings.HasPrefix(r.URL.Path, incidentServicePrefix) {
		if h.reliability == nil {
			http.NotFound(w, r)
			return
		}
		h.gate(w, r, h.reliability)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/workos.") {
		http.NotFound(w, r)
		return
	}
	h.serveStatic(w, r)
}

// serveProduction is the fail-closed edge: Host/Origin policy, session
// resolution for every gated path, local auth services, and the anonymous
// static shell.
func (h *Handler) serveProduction(w http.ResponseWriter, r *http.Request) {
	// Every non-health public request must target exactly the configured
	// public origin host; neither Forwarded headers nor the TLS layer can
	// relax this.
	if r.Host != h.originHost {
		http.Error(w, "request origin rejected", http.StatusForbidden)
		return
	}
	// Cross-site browser requests are rejected before any session work:
	// unsafe methods must present the exact public Origin, and Fetch-
	// Metadata may never declare another site — SameSite=Strict is defense
	// in depth, never the only check.
	if !h.enforceOriginPolicy(w, r) {
		return
	}
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, h.pairingPath):
		h.servePairing(w, r)
		return
	case strings.HasPrefix(path, h.devicePath):
		identity, ok := h.requireSession(w, r)
		if !ok {
			return
		}
		h.serveLocalConnect(w, r.WithContext(identity), h.auth.Device)
		return
	case publicConnectPath(path):
		identity, ok := h.requireSession(w, r)
		if !ok {
			return
		}
		h.proxy.ServeHTTP(w, r.WithContext(identity))
		return
	case runtimeConnectPath(path):
		identity, ok := h.requireSession(w, r)
		if !ok {
			return
		}
		h.runtime.ServeHTTP(w, r.WithContext(identity))
		return
	case strings.HasPrefix(path, surfaceAssetPrefix):
		identity, ok := h.requireSession(w, r)
		if !ok {
			return
		}
		h.runtime.ServeHTTP(w, r.WithContext(identity))
		return
	case strings.HasPrefix(path, incidentServicePrefix):
		if h.reliability == nil {
			http.NotFound(w, r)
			return
		}
		identity, ok := h.requireSession(w, r)
		if !ok {
			return
		}
		h.reliability.ServeHTTP(w, r.WithContext(identity))
		return
	case strings.HasPrefix(path, "/workos."):
		// Private services — including the admin service — are never
		// reachable over TCP, deterministically.
		http.NotFound(w, r)
		return
	default:
		h.serveStatic(w, r)
	}
}

// servePairing runs the anonymous-but-TLS-only device pairing and session
// proof endpoints: request-body budget, remote-IP rate limiting, and exact
// Origin policy for browser posts, then the local Connect handler.
func (h *Handler) servePairing(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, authMaxBodyBytes)
	}
	// RemoteAddr only: X-Forwarded-For is untrusted and never consulted.
	// Malformed or absent addresses share a fail-closed bucket rather than
	// bypassing the limiter. Every request consumes both budgets.
	remoteAllowed := h.auth.RemoteLimiter.Allow(remoteRateKey(r.RemoteAddr))
	globalAllowed := h.auth.GlobalLimiter.Allow("anonymous-auth")
	if !remoteAllowed || !globalAllowed {
		http.Error(w, "too many attempts, retry later", http.StatusTooManyRequests)
		return
	}
	h.serveLocalConnect(w, r, h.auth.Pairing)
}

func remoteRateKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		return host
	}
	if ip := net.ParseIP(strings.TrimSpace(remoteAddr)); ip != nil {
		return ip.String()
	}
	return "unknown"
}

// requireSession resolves the __Host- session cookie to the trusted
// identity, without any process-local cache. Store outages surface as 503;
// missing or invalid cookies as 401 with the cookie cleared; in every
// failure case no upstream is contacted.
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	cookie, err := r.Cookie(authtransport.SessionCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, "device session required", http.StatusUnauthorized)
		return nil, false
	}
	session, resolveErr := h.auth.Service.ResolveSession(r.Context(), cookie.Value)
	if resolveErr != nil {
		switch {
		case errors.Is(resolveErr, domain.ErrStoreUnavailable):
			http.Error(w, "gateway auth unavailable", http.StatusServiceUnavailable)
			return nil, false
		case errors.Is(resolveErr, domain.ErrAuthenticationFailed):
			authtransport.ClearSessionCookie(w)
			http.Error(w, "device session required", http.StatusUnauthorized)
			return nil, false
		default:
			http.Error(w, "device authentication failed", http.StatusInternalServerError)
			return nil, false
		}
	}
	ctx := identity.WithContext(r.Context(), identity.Identity{UserID: session.OwnerID, DeviceID: session.DeviceID})
	ctx = authtransport.WithSessionContext(ctx, authtransport.SessionContext{Identity: session, SessionExpiry: session.ExpiresAt})
	return ctx, true
}

// enforceOriginPolicy rejects cross-site browser requests: unsafe methods
// must present the exact public Origin, and Fetch-Metadata may never
// declare another site.
func (h *Handler) enforceOriginPolicy(w http.ResponseWriter, r *http.Request) bool {
	if isUnsafeMethod(r.Method) {
		origin := r.Header.Get("Origin")
		if origin != h.config.Auth.PublicOrigin {
			http.Error(w, "request origin rejected", http.StatusForbidden)
			return false
		}
	}
	switch site := r.Header.Get("Sec-Fetch-Site"); site {
	case "", "same-origin", "none":
		return true
	default:
		http.Error(w, "request origin rejected", http.StatusForbidden)
		return false
	}
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

// serveLocalConnect mounts one Gateway-local Connect handler with the live
// ResponseWriter injected so completion responses can set the one-time
// session cookie.
func (h *Handler) serveLocalConnect(w http.ResponseWriter, r *http.Request, handler http.Handler) {
	handler.ServeHTTP(w, r.WithContext(authtransport.WithResponseWriter(r.Context(), w)))
}

// gate enforces the development identity before proxying; production mode
// never reaches this path.
func (h *Handler) gate(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy) {
	if !h.config.Auth.DevBypass {
		http.Error(w, "device session required", http.StatusUnauthorized)
		return
	}
	ctx := identity.WithContext(r.Context(), identity.Identity{UserID: h.config.Auth.OwnerID, DeviceID: h.config.Auth.DeviceID})
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

func publicConnectPath(path string) bool {
	for _, prefix := range publicServicePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func runtimeConnectPath(path string) bool {
	for _, prefix := range runtimeServicePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	// The pairing shell and every SPA document must never leak referrers.
	w.Header().Set("Referrer-Policy", "no-referrer")
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." {
		path = "index.html"
	}
	root := os.DirFS(h.config.HTTP.StaticDir)
	data, err := fs.ReadFile(root, path)
	if err != nil {
		path = "index.html"
		data, err = fs.ReadFile(root, "index.html")
		w.Header().Set("Cache-Control", "no-store")
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<!doctype html><title>WorkOS</title><h1>Desktop build unavailable</h1><p>Run make web-build.</p>"))
		return
	}
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	// Static caching boundary: Vite content-hashed assets are immutable and
	// cacheable forever; the HTML shell, manifest, and icons stay no-store so
	// a deploy (and its new asset hashes) lands on the next load.
	if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	_, _ = w.Write(data)
}
