package transport

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
