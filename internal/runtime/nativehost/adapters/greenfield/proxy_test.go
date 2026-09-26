package greenfield

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const proxyTestSession = "018f1a00-0000-7000-8000-000000000001"
const proxyTestOwner = "018f1a00-0000-7000-8000-000000000002"

func withProxyIdentity(r *http.Request, owner string) *http.Request {
	return r.WithContext(identity.WithContext(r.Context(), identity.Identity{UserID: owner, DeviceID: "018f1a00-0000-7000-8000-000000000003"}))
}

func TestServeProxyRewritesLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"signalURL":"ws://127.0.0.1:`+r.Host[strings.LastIndex(r.Host, ":")+1:]+`/signal"}`)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "http://")
	display := &display{localBase: host, compositorSession: "workos", ownerUserID: proxyTestOwner, publicBaseURL: "wss://workos.example/native/greenfield/" + proxyTestSession}
	display.BindController("018f1a00-0000-7000-8000-000000000003", func() bool { return true })
	registerSession(proxyTestSession, display)
	t.Cleanup(func() { unregisterSession(proxyTestSession) })

	request := withProxyIdentity(httptest.NewRequest(http.MethodGet, "/native/greenfield/"+proxyTestSession+"/probe", nil), proxyTestOwner)
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

func TestServeProxyAttachesExistingPIDWithoutLaunchingAgain(t *testing.T) {
	launches := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		launches++
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(upstream.Close)
	display := &display{
		localBase: strings.TrimPrefix(upstream.URL, "http://"), appPID: os.Getpid(), key: "existing-key",
		compositorSession: proxyTestSession, publicBaseURL: "wss://workos.example/native/greenfield/" + proxyTestSession,
		ownerUserID: proxyTestOwner,
	}
	display.BindController("018f1a00-0000-7000-8000-000000000003", func() bool { return true })
	registerSession(proxyTestSession, display)
	t.Cleanup(func() { unregisterSession(proxyTestSession) })
	request := withProxyIdentity(httptest.NewRequest(http.MethodGet, "/native/greenfield/"+proxyTestSession+"/code", nil), proxyTestOwner)
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		ServeProxy(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("attach status %d", recorder.Code)
		}
		var got struct {
			PID       string `json:"pid"`
			SignalURL string `json:"signalURL"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.PID != strconv.Itoa(os.Getpid()) || !strings.HasPrefix(got.SignalURL, "wss://workos.example/native/greenfield/"+proxyTestSession+"/signal?") {
			t.Fatalf("attach response: %+v", got)
		}
	}
	if launches != 0 {
		t.Fatalf("attach launched %d additional apps", launches)
	}
	denied := httptest.NewRecorder()
	ServeProxy(denied, withProxyIdentity(request, "another-owner"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("wrong owner status %d", denied.Code)
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

func TestServeProxyRequiresLiveController(t *testing.T) {
	allowed := &atomic.Bool{}
	allowed.Store(true)
	d := &display{
		appPID: os.Getpid(), key: "existing-key", ownerUserID: proxyTestOwner,
		publicBaseURL: "wss://workos.example/native/greenfield/" + proxyTestSession,
	}
	d.BindController("018f1a00-0000-7000-8000-000000000003", allowed.Load)
	registerSession(proxyTestSession, d)
	t.Cleanup(func() { unregisterSession(proxyTestSession) })
	path := "/native/greenfield/" + proxyTestSession + "/code"

	observer := httptest.NewRequest(http.MethodGet, path, nil)
	observer = observer.WithContext(identity.WithContext(observer.Context(), identity.Identity{
		UserID: proxyTestOwner, DeviceID: "018f1a00-0000-7000-8000-000000000004",
	}))
	response := httptest.NewRecorder()
	ServeProxy(response, observer)
	if response.Code != http.StatusForbidden {
		t.Fatalf("observer got status %d", response.Code)
	}

	allowed.Store(false)
	response = httptest.NewRecorder()
	ServeProxy(response, withProxyIdentity(httptest.NewRequest(http.MethodGet, path, nil), proxyTestOwner))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expired controller got status %d", response.Code)
	}
}

func TestServeProxyRevokesOpenWebSocket(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			kind, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			if err := conn.Write(r.Context(), kind, payload); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	allowed := &atomic.Bool{}
	allowed.Store(true)
	d := &display{localBase: strings.TrimPrefix(upstream.URL, "http://"), ownerUserID: proxyTestOwner}
	d.BindController("018f1a00-0000-7000-8000-000000000003", allowed.Load)
	registerSession(proxyTestSession, d)
	t.Cleanup(func() { unregisterSession(proxyTestSession) })
	proxy := httptest.NewServer(identity.Middleware(http.HandlerFunc(ServeProxy)))
	t.Cleanup(proxy.Close)
	d.publicBaseURL = "ws" + strings.TrimPrefix(proxy.URL, "http") + "/native/greenfield/" + proxyTestSession
	headers := http.Header{}
	headers.Set(identity.UserHeader, proxyTestOwner)
	headers.Set(identity.DeviceHeader, "018f1a00-0000-7000-8000-000000000003")
	headers.Set("Origin", proxy.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, d.publicBaseURL+"/channel", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("first")); err != nil {
		t.Fatal(err)
	}
	_, reply, err := conn.Read(ctx)
	if err != nil || string(reply) != "first" {
		t.Fatalf("relay reply %q: %v", reply, err)
	}
	allowed.Store(false)
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("expired controller websocket remained open")
	}
}

func TestKillSignalCannotBypassRuntimeStop(t *testing.T) {
	if !isKillAppSignal([]byte(`{"type":4,"data":{"signal":"SIGTERM"}}`)) {
		t.Fatal("KILL_APP signal was not recognized")
	}
	for _, message := range [][]byte{[]byte(`{"type":0}`), []byte(`{"type":3}`), []byte(`invalid`)} {
		if isKillAppSignal(message) {
			t.Fatalf("non-kill signaling rejected: %s", message)
		}
	}
}
