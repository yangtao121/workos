package greenfield

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
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
	if display == nil || display.stopped {
		http.Error(w, "greenfield display unavailable", http.StatusServiceUnavailable)
		return
	}
	target := &url.URL{Scheme: "http", Host: display.localBase}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.URL.Path = "/" + suffix
		req.URL.RawPath = ""
		if req.URL.RawQuery != "" {
			req.URL.RawQuery = req.URL.RawQuery
		}
		req.Host = target.Host
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		origin := resp.Request.Header.Get("WorkOS-Public-Origin")
		if origin == "" || resp.Body == nil || resp.Header.Get("Content-Type") == "" {
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
		wsOrigin := "ws" + strings.TrimPrefix(origin, "http")
		public := strings.TrimRight(origin, "/") + proxyPrefix + sessionID
		wsPublic := strings.TrimRight(wsOrigin, "/") + proxyPrefix + sessionID
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
