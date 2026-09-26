package gateway

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/yangtao121/workos/internal/gateway/auth/transport"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func TestSurfaceWebSocketClosesAfterDeviceRevocation(t *testing.T) {
	closed := make(chan struct{})
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(identity.UserHeader) != testOwnerID || r.Header.Get(identity.DeviceHeader) != testDeviceID {
			t.Error("untrusted runtime proxy identity")
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept runtime socket: %v", err)
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
		close(closed)
	}))
	defer runtime.Close()
	store := &revokingStreamStore{gateStore: newGateStore(true)}
	handler, err := New(config.Config{Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: runtime.URL}, Auth: config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin}}, newTestLogger(), newTestAuthStack(t, store))
	if err != nil {
		t.Fatal(err)
	}
	handler.streamRevalidation = 5 * time.Millisecond
	gateway := httptest.NewServer(handler)
	defer gateway.Close()
	addr := strings.TrimPrefix(gateway.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /surfaces/native-session HTTP/1.1\r\nHost: workos.example\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: MDEyMzQ1Njc4OWFiY2RlZg==\r\nCookie: %s=%s\r\n\r\n", testOrigin, transport.SessionCookieName, testSessionToken)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status: %s", response.Status)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("runtime websocket remained open after session revocation")
	}
	if store.calls.Load() < 2 {
		t.Fatal("websocket did not revalidate session")
	}
}

func TestProductionRawGreenfieldProxyIsNotRouted(t *testing.T) {
	called := make(chan struct{}, 1)
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer runtime.Close()
	store := newGateStore(true)
	handler, err := New(config.Config{Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: runtime.URL}, Auth: config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin}}, newTestLogger(), newTestAuthStack(t, store))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, testOrigin+"/native/greenfield/session/code", nil)
	r.AddCookie(&http.Cookie{Name: transport.SessionCookieName, Value: testSessionToken})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("direct Greenfield proxy returned %d", w.Code)
	}
	select {
	case <-called:
		t.Fatal("raw proxy request reached Runtime")
	default:
	}
}

func TestCrossOriginNativeWebSocketRejected(t *testing.T) {
	store := newGateStore(true)
	handler, err := New(config.Config{Services: config.URLs{Core: "http://127.0.0.1:1", Runtime: "http://127.0.0.1:1"}, Auth: config.Auth{OwnerID: testOwnerID, PublicOrigin: testOrigin}}, newTestLogger(), newTestAuthStack(t, store))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, testOrigin+"/native/greenfield/session", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin upgrade returned %d", w.Code)
	}
}
