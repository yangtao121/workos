package gateway

import (
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// Handler routes the public edge: allowlisted public Connect services and the
// Desktop SPA to Core, the public SurfaceService and /surfaces/ assets to the
// Runtime upstream. Every proxied path passes the same device-session gate
// and receives trusted identity headers; spoofed inbound copies are dropped.
type Handler struct {
	config  config.Config
	proxy   *httputil.ReverseProxy
	runtime *httputil.ReverseProxy
	logger  *slog.Logger
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

// surfaceAssetPrefix is the public, same-origin surface asset route.
const surfaceAssetPrefix = "/surfaces/"

func New(cfg config.Config, logger *slog.Logger) (*Handler, error) {
	core, err := newUpstreamProxy(cfg.Services.Core, cfg, logger, "core")
	if err != nil {
		return nil, err
	}
	runtime, err := newUpstreamProxy(cfg.Services.Runtime, cfg, logger, "runtime")
	if err != nil {
		return nil, err
	}
	return &Handler{config: cfg, proxy: core, runtime: runtime, logger: logger}, nil
}

// newUpstreamProxy builds one reverse proxy whose director always drops
// client-supplied identity headers and re-injects the trusted owner/device
// pair, so spoofing the inbound request can never reach an upstream. The
// bridge token header is scoped to the Runtime Connect routes only: the Core
// director strips it, so the credential can never travel to Core services or
// the Desktop SPA static handler, and nothing logs it. The constructor
// rejects targets that could never work — relative paths, scheme-less or
// unsupported-scheme strings, empty hosts — instead of deferring the failure
// to the first request, even when composition-root validation was bypassed.
func newUpstreamProxy(target string, cfg config.Config, logger *slog.Logger, name string) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(target)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid %s URL: must be an absolute http(s) URL with a host", name)
	}
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Header.Del(identity.UserHeader)
		request.Header.Del(identity.DeviceHeader)
		request.Header.Set(identity.UserHeader, cfg.Auth.OwnerID)
		request.Header.Set(identity.DeviceHeader, cfg.Auth.DeviceID)
		// The bridge credential is stripped by default on every route — Core
		// public/private RPCs, SurfaceService, /surfaces/ assets, Desktop
		// static/fallback — and re-attached only on the public AppBridge
		// Connect routes, so a malicious client cannot move a bridge token
		// sideways through any other WorkOS path. The path predicate decides,
		// never the upstream name or what the client "normally" sends.
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
	if publicConnectPath(r.URL.Path) {
		h.gate(w, r, h.proxy)
		return
	}
	if runtimeConnectPath(r.URL.Path) || strings.HasPrefix(r.URL.Path, surfaceAssetPrefix) {
		h.gate(w, r, h.runtime)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/workos.") {
		http.NotFound(w, r)
		return
	}
	h.serveStatic(w, r)
}

// gate enforces the device session before proxying: without a real device
// session (development bypass off) every public API path fails closed.
func (h *Handler) gate(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy) {
	if !h.config.Auth.DevBypass {
		http.Error(w, "device session required", http.StatusUnauthorized)
		return
	}
	proxy.ServeHTTP(w, r)
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
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." {
		path = "index.html"
	}
	root := os.DirFS(h.config.HTTP.StaticDir)
	data, err := fs.ReadFile(root, path)
	if err != nil {
		path = "index.html"
		data, err = fs.ReadFile(root, "index.html")
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
	_, _ = w.Write(data)
}
