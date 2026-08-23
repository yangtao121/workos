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
	request := httptest.NewRequest(http.MethodPost, "/workos.project.v1.ProjectService/ListProjects", nil)
	request.Header.Set(identity.UserHeader, "attacker")
	request.Header.Set(identity.DeviceHeader, "attacker")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected response %d", response.Code)
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
	var coreCalled atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { coreCalled.Store(true) }))
	defer core.Close()
	handler := newTestHandler(t, config.Config{
		Services: config.URLs{Core: core.URL},
		Auth:     config.Auth{DevBypass: true, OwnerID: "owner-1", DeviceID: "device-1"},
	})
	for _, path := range []string{
		"/workos.harness.v1.HarnessHostService/DescribeProviders",
		"/workos.harness.v1.HarnessHostService/ExecuteTask",
		"/workos.harness.v1.HarnessHostService/CancelRun",
		"/workos.taskexecution.v1.TaskExecutionService/ClaimTask",
		"/workos.surface.v1.SurfaceService/CreateSurface",
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
	handler, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
