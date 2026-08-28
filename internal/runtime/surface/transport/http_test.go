package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

const (
	httpOwner  = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	httpDevice = "0198d7ea-2110-7c42-b659-c5e4d73bc338"
)

// assetFixture is a minimal AssetService over a map; deeper session semantics
// are covered by the application tests.
type assetFixture struct {
	assets  map[string]ports.Asset
	unavail bool
}

func (f *assetFixture) ServeAsset(_ context.Context, owner, device, sessionID, path string) (ports.Asset, error) {
	if f.unavail {
		return ports.Asset{}, domain.ErrUnavailable
	}
	if owner != httpOwner || device != httpDevice || sessionID != "0198d7ea-2110-7c42-b659-c5e4d73bc341" {
		return ports.Asset{}, domain.ErrNotFound
	}
	asset, ok := f.assets[path]
	if !ok {
		return ports.Asset{}, domain.ErrNotFound
	}
	return asset, nil
}

func newAssetServer(t *testing.T, fixture *assetFixture) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(identity.Middleware(NewAssetHandler(fixture, logger)))
	t.Cleanup(server.Close)
	return server
}

func TestAssetHandlerServesBoundedAssetWithSecurityHeaders(t *testing.T) {
	t.Parallel()
	server := newAssetServer(t, &assetFixture{assets: map[string]ports.Asset{
		"": {Content: []byte("<p>entry</p>"), MediaType: "text/html; charset=utf-8", Etag: "sha256:" + strings.Repeat("a", 64)},
	}})
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", nil)
	request.Header.Set(identity.UserHeader, httpOwner)
	request.Header.Set(identity.DeviceHeader, httpDevice)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("entrypoint status %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("server media type missing: %q", got)
	}
	csp := response.Header.Get("Content-Security-Policy")
	for _, required := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'none'", "frame-ancestors 'self'", "worker-src 'none'", "sandbox allow-scripts"} {
		if !strings.Contains(csp, required) {
			t.Fatalf("CSP missing %q: %q", required, csp)
		}
	}
	// The server-enforced sandbox must never re-grant origin power: no
	// same-origin, forms, popups, top navigation, downloads, or storage.
	for _, forbidden := range []string{"allow-same-origin", "allow-forms", "allow-popups", "allow-top-navigation", "allow-downloads", "allow-storage-access", "allow-modals"} {
		if strings.Contains(csp, forbidden) {
			t.Fatalf("CSP grants dangerous sandbox token %q: %q", forbidden, csp)
		}
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" ||
		response.Header.Get("Referrer-Policy") != "no-referrer" ||
		response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("hardening headers missing")
	}
	if response.Header.Get("ETag") == "" {
		t.Fatal("etag missing")
	}
}

// TestAssetHandlerSandboxesEveryResponse proves the CSP sandbox is a
// server-enforced invariant on every document this route can emit — success,
// not-found, and unavailable alike — for HTML and every other served MIME,
// so the response stays an opaque-origin document even when the surface URL
// is opened outside the Desktop iframe.
func TestAssetHandlerSandboxesEveryResponse(t *testing.T) {
	t.Parallel()
	server := newAssetServer(t, &assetFixture{assets: map[string]ports.Asset{
		"":         {Content: []byte("<p>entry</p>"), MediaType: "text/html; charset=utf-8", Etag: "e1"},
		"icon.svg": {Content: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), MediaType: "image/svg+xml", Etag: "e2"},
	}})
	do := func(path string) *http.Response {
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		request.Header.Set(identity.UserHeader, httpOwner)
		request.Header.Set(identity.DeviceHeader, httpDevice)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() }) //nolint:errcheck
		return response
	}
	for name, path := range map[string]string{
		"html entrypoint": "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/",
		"svg asset":       "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/icon.svg",
		"missing asset":   "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/missing.js",
		"unknown session": "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc399/",
	} {
		csp := do(path).Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "sandbox allow-scripts") {
			t.Errorf("%s response lacks the sandbox directive: %q", name, csp)
		}
		if strings.Contains(csp, "allow-same-origin") {
			t.Errorf("%s response re-grants origin power: %q", name, csp)
		}
	}
	// Unavailability keeps the sandbox too: the outage page is equally an
	// opaque document.
	server503 := newAssetServer(t, &assetFixture{unavail: true})
	request, _ := http.NewRequest(http.MethodGet, server503.URL+"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", nil)
	request.Header.Set(identity.UserHeader, httpOwner)
	request.Header.Set(identity.DeviceHeader, httpDevice)
	response, err := server503.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close() //nolint:errcheck
	if csp := response.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts") {
		t.Fatalf("503 response lacks the sandbox directive: %q", csp)
	}
}

func TestAssetHandlerMethodAndPathPolicy(t *testing.T) {
	t.Parallel()
	server := newAssetServer(t, &assetFixture{assets: map[string]ports.Asset{
		"": {Content: []byte("x"), MediaType: "text/html; charset=utf-8", Etag: "e"},
	}})
	client := server.Client()
	do := func(method, path string, withIdentity bool) int {
		request, _ := http.NewRequest(method, server.URL+path, nil)
		if withIdentity {
			request.Header.Set(identity.UserHeader, httpOwner)
			request.Header.Set(identity.DeviceHeader, httpDevice)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if got := do(http.MethodPost, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", true); got != http.StatusMethodNotAllowed {
		t.Fatalf("POST status %d", got)
	}
	if got := do(http.MethodPut, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/app.js", true); got != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status %d", got)
	}
	if got := do(http.MethodHead, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", true); got != http.StatusOK {
		t.Fatalf("HEAD status %d", got)
	}
	for name, path := range map[string]string{
		"unknown session":   "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc399/",
		"malformed id":      "/surfaces/not-a-uuid/",
		"traversal":         "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/../../secret",
		"encoded traversal": "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/%2e%2e/secret",
		"double encoding":   "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/%252e%252e/secret",
		"backslash":         `/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/\..\secret`,
		"dot segment":       "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/./app.js",
		"dotdot segment":    "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/../app.js",
		"double slash":      "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341//app.js",
	} {
		if got := do(http.MethodGet, path, true); got != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", name, got)
		}
	}
	// Missing identity fails closed with a plain 404.
	if got := do(http.MethodGet, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", false); got != http.StatusNotFound {
		t.Fatalf("missing identity status %d", got)
	}
}

func TestAssetHandlerMapsUnavailableOnly(t *testing.T) {
	t.Parallel()
	server := newAssetServer(t, &assetFixture{unavail: true})
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc341/", nil)
	request.Header.Set(identity.UserHeader, httpOwner)
	request.Header.Set(identity.DeviceHeader, httpDevice)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("core unavailability status %d", response.StatusCode)
	}
}
