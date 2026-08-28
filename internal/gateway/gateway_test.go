package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func TestDevelopmentProxyReplacesSpoofedIdentity(t *testing.T) {
	t.Parallel()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get(identity.UserHeader); got != "owner-1" {
			t.Errorf("unexpected user identity %q", got)
		}
		if got := request.Header.Get(identity.DeviceHeader); got != "device-1" {
			t.Errorf("unexpected device identity %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer core.Close()
	handler := newTestHandler(t, config.Config{
		Services: config.URLs{Core: core.URL},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	})
	for _, path := range []string{
		"/workos.project.v1.ProjectService/ListProjects",
		"/workos.app.v1.AppInstallationService/InstallApp",
		"/workos.app.v1.AppInstallationService/UninstallApp",
		"/workos.app.v1.AppInstallationService/ListInstalledApps",
	} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		request.Header.Set(identity.UserHeader, "attacker")
		request.Header.Set(identity.DeviceHeader, "attacker")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("path %s: unexpected response %d", path, response.Code)
		}
	}
}

func TestPublicAPIRejectsMissingDeviceSession(t *testing.T) {
	t.Parallel()
	var coreCalled atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { coreCalled.Store(true) }))
	defer core.Close()
	handler := newTestHandler(t, config.Config{Services: config.URLs{Core: core.URL}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/workos.agent.v1.AgentTaskService/GetTask", nil))
	if response.Code != http.StatusUnauthorized || coreCalled.Load() {
		t.Fatalf("expected fail-closed request, status=%d coreCalled=%v", response.Code, coreCalled.Load())
	}
}

func TestPrivateConnectServicesAreNotForwarded(t *testing.T) {
	t.Parallel()
	var coreCalled, runtimeCalled atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { coreCalled.Store(true) }))
	defer core.Close()
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalled.Store(true) }))
	defer runtime.Close()
	handler := newTestHandler(t, config.Config{
		Services: config.URLs{Core: core.URL, Runtime: runtime.URL},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	})
	for _, path := range []string{
		"/workos.harness.v1.HarnessHostService/DescribeProviders",
		"/workos.harness.v1.HarnessHostService/ExecuteTask",
		"/workos.harness.v1.HarnessHostService/CancelRun",
		"/workos.taskexecution.v1.TaskExecutionService/ClaimTask",
		"/workos.surface.v1.SurfaceLaunchResolverService/ResolveWebBundle",
		"/workos.workload.v1.WorkloadService/StartWorkload",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("private path %s returned %d", path, response.Code)
		}
	}
	if coreCalled.Load() {
		t.Fatal("gateway forwarded a private Connect service")
	}
	if runtimeCalled.Load() {
		t.Fatal("gateway forwarded a private Connect service to the runtime upstream")
	}
}

func TestSurfaceRoutesReachRuntimeWithTrustedIdentity(t *testing.T) {
	t.Parallel()
	var runtimeCalled atomic.Bool
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get(identity.UserHeader); got != "owner-1" {
			t.Errorf("runtime upstream saw spoofed user identity %q", got)
		}
		if got := request.Header.Get(identity.DeviceHeader); got != "device-1" {
			t.Errorf("runtime upstream saw spoofed device identity %q", got)
		}
		runtimeCalled.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer runtime.Close()
	handler := newTestHandler(t, config.Config{
		Services: config.URLs{Runtime: runtime.URL},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	})
	for _, path := range []string{
		"/workos.surface.v1.SurfaceService/CreateSurface",
		"/workos.surface.v1.SurfaceService/CloseSurface",
		"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc337/",
		"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc337/app.js",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(identity.UserHeader, "attacker")
		request.Header.Set(identity.DeviceHeader, "attacker")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("surface path %s returned %d", path, response.Code)
		}
	}
	if !runtimeCalled.Load() {
		t.Fatal("surface routes did not reach the runtime upstream")
	}
}

func TestSurfaceRoutesRequireDeviceSession(t *testing.T) {
	t.Parallel()
	var runtimeCalled atomic.Bool
	runtime := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { runtimeCalled.Store(true) }))
	defer runtime.Close()
	handler := newTestHandler(t, config.Config{Services: config.URLs{Runtime: runtime.URL}})
	for _, path := range []string{
		"/workos.surface.v1.SurfaceService/CreateSurface",
		"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc337/",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized || runtimeCalled.Load() {
			t.Fatalf("surface path %s did not fail closed: status=%d runtimeCalled=%v", path, response.Code, runtimeCalled.Load())
		}
	}
}

func TestRuntimeUpstreamFailureIsSanitized(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, config.Config{
		Services: config.URLs{Runtime: "http://127.0.0.1:1"},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc337/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable runtime returned %d", response.Code)
	}
	if body := response.Body.String(); body != "workos runtime unavailable\n" {
		t.Fatalf("unsanitized runtime failure body %q", body)
	}
}

func TestSPAFallbackUsesHTMLContentType(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<!doctype html><title>WorkOS</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t, config.Config{
		HTTP:     config.HTTP{StaticDir: directory},
		Services: config.URLs{Core: "http://127.0.0.1:1"},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/projects/active.js", nil))
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("unexpected fallback content type %q", got)
	}
}

func newTestHandler(t *testing.T, cfg config.Config) *Handler {
	t.Helper()
	// Tests exercise routing behavior, not upstream validation; only a test
	// that targets validation sets an invalid upstream explicitly.
	if cfg.Services.Core == "" {
		cfg.Services.Core = "http://127.0.0.1:1"
	}
	if cfg.Services.Runtime == "" {
		cfg.Services.Runtime = "http://127.0.0.1:1"
	}
	handler, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestNewRejectsUnusableUpstreamTargets(t *testing.T) {
	t.Parallel()
	for name, target := range map[string]string{
		"empty":         "",
		"relative path": "runtime-host/surfaces/",
		"scheme-less":   "127.0.0.1:8083",
		"unsupported":   "ftp://127.0.0.1:8083",
		"missing host":  "http://",
	} {
		cfg := config.Config{
			Services: config.URLs{Core: "http://127.0.0.1:8081", Runtime: target},
			Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
		}
		if _, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("runtime URL %q (%s) accepted by the proxy constructor", target, name)
		}
	}
	core := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer core.Close()
	cfg := config.Config{
		Services: config.URLs{Core: core.URL, Runtime: "not-a-target"},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	}
	if _, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("invalid runtime URL accepted alongside a valid core URL")
	}
}
