package transport

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type servingFixture struct {
	calls   int
	request ports.Request
	token   string
}

func (f *servingFixture) Request(_ context.Context, _ string, token string, r ports.Request) (ports.Response, error) {
	f.calls++
	f.request = r
	f.token = token
	if token != strings.Repeat("a", 64) {
		return ports.Response{}, errors.New("bad token")
	}
	return ports.Response{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"}, Body: []byte("served")}, nil
}
func TestPreviewProxyConfinesCapabilityAndStripsCredentials(t *testing.T) {
	fixture := &servingFixture{}
	handler := NewServingHandler(fixture, slog.New(slog.NewTextHandler(io.Discard, nil)))
	prefix := "/previews/0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa/" + strings.Repeat("a", 64) + "/"
	request := httptest.NewRequest("POST", prefix+"state?q=fixture", strings.NewReader("value"))
	request.Header.Set("Cookie", "owner=session")
	request.Header.Set("Authorization", "secret")
	request.Header.Set("X-WorkOS-User-ID", "forged")
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || fixture.request.Method != "POST" || fixture.request.Path != "/state" || fixture.request.Query != "q=fixture" {
		t.Fatal("proxy changed request")
	}
	for _, key := range []string{"Cookie", "Authorization", "X-WorkOS-User-ID"} {
		if fixture.request.Headers[key] != "" {
			t.Fatal("credential escaped into development server")
		}
	}
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts allow-forms") || strings.Contains(csp, "allow-same-origin") {
		t.Fatal("preview lost opaque origin")
	}
	for _, path := range []string{prefix + "../escape", prefix + "%2e%2e/escape", prefix + "%5cescape", "/previews/invalid/", strings.Replace(prefix, strings.Repeat("a", 64), strings.Repeat("b", 64), 1)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 404 {
			t.Fatalf("bad route accepted: %d", response.Code)
		}
	}
}
