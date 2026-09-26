package greenfield

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const proxyPrefix = "/native/greenfield/"

var (
	sessionsMu sync.Mutex
	sessions   = map[string]*display{}
)

func registerSession(id string, display *display) {
	sessionsMu.Lock()
	sessions[id] = display
	sessionsMu.Unlock()
}

func unregisterSession(id string) {
	sessionsMu.Lock()
	delete(sessions, id)
	sessionsMu.Unlock()
}

func lookupSession(id string) *display {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	return sessions[id]
}

// ServeProxy forwards an authenticated /native/greenfield/{session}/ request
// to that session's loopback compositor proxy. Loopback addresses in the
// proxy JSON are rewritten to WorkOS-Public-Origin.
func ServeProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, proxyPrefix)
	sessionID, suffix, ok := strings.Cut(rest, "/")
	if !ok || sessionID == "" {
		http.NotFound(w, r)
		return
	}
	display := lookupSession(sessionID)
	if display == nil {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	caller, err := identity.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if caller.UserID != display.ownerUserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// A reusable Greenfield signaling key must never grant an observer or a
	// superseded controller access. Recheck the live lease on every request.
	if !display.canProxy(caller.DeviceID) {
		http.Error(w, "control lease required", http.StatusForbidden)
		return
	}
	if suffix == "code" {
		serveExistingApp(w, r, display)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		serveWebSocketProxy(w, r, display, caller.DeviceID, suffix)
		return
	}
	target := &url.URL{Scheme: "http", Host: display.localBase}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.URL.Path = "/" + suffix
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Body == nil || resp.Header.Get("Content-Type") == "" {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		wsPublic := display.publicBaseURL
		public := strings.Replace(wsPublic, "ws", "http", 1)
		body = bytes.ReplaceAll(body, []byte("http://"+display.localBase), []byte(public))
		body = bytes.ReplaceAll(body, []byte("ws://"+display.localBase), []byte(wsPublic))
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", "")
		resp.Header.Del("Content-Length")
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
	}
	proxy.ServeHTTP(w, r)
}

// Greenfield carries Wayland control events on its WebSocket channels. A
// standard HTTP reverse proxy checks authorization only at upgrade time and
// would let a superseded controller keep injecting input. Relay each frame
// so lease expiry and takeover revoke an already established connection.
func serveWebSocketProxy(w http.ResponseWriter, r *http.Request, d *display, deviceID, suffix string) {
	upstreamURL := url.URL{Scheme: "ws", Host: d.localBase, Path: "/" + suffix, RawQuery: r.URL.RawQuery}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, _, err := websocket.Dial(ctx, upstreamURL.String(), nil)
	if err != nil {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	defer upstream.CloseNow()
	publicURL, err := url.Parse(d.publicBaseURL)
	if err != nil || publicURL.Host == "" {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{publicURL.Host},
	})
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(64 << 20)
	upstream.SetReadLimit(64 << 20)

	done := make(chan struct{}, 2)
	go func() {
		for {
			kind, payload, err := browser.Read(ctx)
			if err != nil {
				break
			}
			if !d.canProxy(deviceID) {
				break
			}
			// The browser library's AppContext.close sends KILL_APP (type 4).
			// Explicit Stop is owned by Runtime; preserve other signaling.
			if suffix == "signal" && kind == websocket.MessageText && isKillAppSignal(payload) {
				continue
			}
			if err := upstream.Write(ctx, kind, payload); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() {
		for {
			kind, payload, err := upstream.Read(ctx)
			if err != nil {
				break
			}
			if !d.canProxy(deviceID) {
				break
			}
			if err := browser.Write(ctx, kind, payload); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !d.canProxy(deviceID) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func isKillAppSignal(payload []byte) bool {
	var message struct {
		Type int `json:"type"`
	}
	return json.Unmarshal(payload, &message) == nil && message.Type == 4
}

// The browser's remote launcher GET is an attach. Forwarding /code upstream
// would spawn a second official Code process on every page load.
func serveExistingApp(w http.ResponseWriter, r *http.Request, d *display) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d.mu.Lock()
	pid, key, baseURL, compositorSession, stopped := d.appPID, d.key, d.publicBaseURL, d.compositorSession, d.stopped
	d.mu.Unlock()
	if stopped || pid <= 0 || key == "" || d.Exited() {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	signalURL, err := url.Parse(baseURL + "/signal")
	if err != nil {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	query := signalURL.Query()
	query.Set("compositorSessionId", compositorSession)
	query.Set("key", key)
	signalURL.RawQuery = query.Encode()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"baseURL":   baseURL,
		"signalURL": signalURL.String(),
		"pid":       strconv.Itoa(pid),
		"key":       key,
		"name":      "Code",
		"internal":  false,
	})
}
