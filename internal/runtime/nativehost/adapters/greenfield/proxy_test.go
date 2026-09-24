package greenfield

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeProxyRewritesLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/code" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"signalURL":"ws://127.0.0.1:`+r.Host[strings.LastIndex(r.Host, ":")+1:]+`/signal"}`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	display := &display{localBase: host, compositorSession: "workos"}
	registerSession("018f1a00-0000-7000-8000-000000000001", display)
	t.Cleanup(func() { unregisterSession("018f1a00-0000-7000-8000-000000000001") })

	request := httptest.NewRequest(http.MethodGet, "/native/greenfield/018f1a00-0000-7000-8000-000000000001/code", nil)
	request.Header.Set("WorkOS-Public-Origin", "https://workos.example")
	recorder := httptest.NewRecorder()
	ServeProxy(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "127.0.0.1") {
		t.Fatalf("loopback leaked: %s", body)
	}
	if !strings.Contains(body, "wss://workos.example/native/greenfield/018f1a00-0000-7000-8000-000000000001/signal") {
		t.Fatalf("rewritten body %s", body)
	}
}

func TestServeProxyMissingSession(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/native/greenfield/missing/code", nil)
	recorder := httptest.NewRecorder()
	ServeProxy(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", recorder.Code)
	}
}
