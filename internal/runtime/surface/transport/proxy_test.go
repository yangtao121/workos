package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type fakeProxyService struct{ target ports.ProxyTarget }

func (f *fakeProxyService) ResolveProxyTarget(context.Context, string, string, string, string) (ports.ProxyTarget, error) {
	return f.target, nil
}

// TestProxyRoundTripStripsAndRewritesRedirects pins the redirect boundary:
// the backend's Location never reaches the client raw; only a rewrite that
// passes the strict path grammar re-attaches one inside the session prefix,
// and cookies plus the fixed CSP posture survive any backend response.
func TestProxyRoundTripStripsAndRewritesRedirects(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name         string
		location     string
		wantLocation string
	}{
		{"external absolute is dropped", "https://evil.example/escape", ""},
		{"protocol-relative is dropped", "//evil.example", ""},
		{"encoded is dropped", "/a%2Fb", ""},
		{"clean relative is rewritten", "next/page", "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc361/next/page"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", testCase.location)
				w.Header().Set("Set-Cookie", "session=attacker")
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusFound)
			}))
			defer backend.Close()
			roundTripper := &defaultProxyRoundTripper{client: newProxyClient(), logger: logger}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/surfaces/0198d7ea-2110-7c42-b659-c5e4d73bc361/", nil)
			roundTripper.roundTrip(recorder, request, ports.ProxyTarget{
				SessionID:   "0198d7ea-2110-7c42-b659-c5e4d73bc361",
				Endpoint:    backend.Listener.Addr().String(),
				BackendPath: "/",
			})
			got := recorder.Header().Get("Location")
			if got != testCase.wantLocation {
				t.Fatalf("Location %q, want %q", got, testCase.wantLocation)
			}
			if cookie := recorder.Header().Get("Set-Cookie"); cookie != "" {
				t.Fatalf("backend cookie reached the client: %q", cookie)
			}
			if recorder.Header().Get("Content-Security-Policy") != surfaceCSP {
				t.Fatalf("CSP was not the fixed WorkOS policy")
			}
		})
	}
}

var _ = domain.ErrNotFound

func TestProxyRejectsOversizedBodyBeforeCommittingBackendStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name    string
		chunked bool
	}{
		{name: "declared length"},
		{name: "unknown length", chunked: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if testCase.chunked {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", strconv.Itoa(proxyBodyLimitBytes+1))
				}
				_, _ = w.Write(bytes.Repeat([]byte{'x'}, proxyBodyLimitBytes+1))
			}))
			defer backend.Close()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			(&defaultProxyRoundTripper{client: newProxyClient(), logger: logger}).roundTrip(recorder, request, ports.ProxyTarget{
				SessionID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", Endpoint: backend.Listener.Addr().String(), BackendPath: "/",
			})
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", recorder.Code)
			}
			if recorder.Header().Get("Content-Security-Policy") != surfaceCSP {
				t.Fatal("rejection response lost fixed security headers")
			}
			if recorder.Body.Len() > 1024 {
				t.Fatalf("oversized backend body leaked %d bytes", recorder.Body.Len())
			}
		})
	}
}

func TestProxyRejectsOversizedHeadersAndConnectionNominatedHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("X-Oversized", string(bytes.Repeat([]byte{'a'}, proxyHeaderLimitBytes)))
	if filterResponseHeaders(recorder, response) {
		t.Fatal("oversized response headers were accepted")
	}

	recorder = httptest.NewRecorder()
	response = &http.Response{Header: make(http.Header)}
	for index := 0; index <= proxyHeaderBudget; index++ {
		response.Header.Add("Set-Cookie", "discarded=value")
	}
	if filterResponseHeaders(recorder, response) {
		t.Fatal("stripped headers bypassed the response header count budget")
	}

	recorder = httptest.NewRecorder()
	response = &http.Response{Header: make(http.Header)}
	for index := 0; index < proxyHeaderBudget; index++ {
		response.Header.Add("X-"+string(bytes.Repeat([]byte{'N'}, 1024)), "v")
	}
	if filterResponseHeaders(recorder, response) {
		t.Fatal("repeated field names bypassed the serialized header byte budget")
	}

	recorder = httptest.NewRecorder()
	response = &http.Response{Header: make(http.Header)}
	response.Header.Set("Connection", "X-Backend-Secret")
	response.Header.Set("X-Backend-Secret", "must-not-cross")
	response.Header.Set("X-Safe", "ok")
	if !filterResponseHeaders(recorder, response) {
		t.Fatal("bounded response headers were rejected")
	}
	if got := recorder.Header().Get("X-Backend-Secret"); got != "" {
		t.Fatalf("Connection-nominated header crossed the proxy: %q", got)
	}
	if got := recorder.Header().Get("X-Safe"); got != "ok" {
		t.Fatalf("safe header %q, want ok", got)
	}
}

func TestProxyRejectsEncodedBodiesAndStripsBrowserControlHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("Content-Encoding", "br")
	if filterResponseHeaders(recorder, response) {
		t.Fatal("encoded backend body bypassed the decoded-size boundary")
	}

	recorder = httptest.NewRecorder()
	response = &http.Response{Header: make(http.Header)}
	for name, value := range map[string]string{
		"Access-Control-Allow-Origin":         "*",
		"Clear-Site-Data":                     `"*"`,
		"Content-Security-Policy-Report-Only": "report-uri https://evil.example/report",
		"Refresh":                             "0;url=https://evil.example",
		"Alt-Svc":                             `h3=":443"`,
	} {
		response.Header.Set(name, value)
	}
	response.Header.Set("Content-Encoding", "identity")
	response.Header.Set("X-Safe", "ok")
	if !filterResponseHeaders(recorder, response) {
		t.Fatal("bounded identity response was rejected")
	}
	for _, name := range []string{
		"Access-Control-Allow-Origin", "Clear-Site-Data",
		"Content-Security-Policy-Report-Only", "Refresh", "Alt-Svc", "Content-Encoding",
	} {
		if got := recorder.Header().Get(name); got != "" {
			t.Fatalf("backend-controlled header %s crossed the proxy: %q", name, got)
		}
	}
	if recorder.Header().Get("X-Safe") != "ok" {
		t.Fatal("safe response header was lost")
	}
}

func TestProxyRejectsAnyNonCanonicalLoopbackTargetBeforeDial(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, target := range []ports.ProxyTarget{
		{SessionID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", Endpoint: "169.254.169.254:80", BackendPath: "/"},
		{SessionID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", Endpoint: "127.0.0.1:080", BackendPath: "/"},
		{SessionID: "0198d7ea-2110-7c42-b659-c5e4d73bc361", Endpoint: "127.0.0.1:80", BackendPath: "/../escape"},
		{SessionID: "not-a-session", Endpoint: "127.0.0.1:80", BackendPath: "/"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		(&defaultProxyRoundTripper{client: newProxyClient(), logger: logger}).roundTrip(recorder, request, target)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("target %+v returned %d, want 503", target, recorder.Code)
		}
	}
}

func TestProxyClientDoesNotNegotiateTransparentCompression(t *testing.T) {
	transport, ok := newProxyClient().Transport.(*http.Transport)
	if !ok || !transport.DisableCompression {
		t.Fatal("proxy transport may add Accept-Encoding or transparently decode responses")
	}
}
