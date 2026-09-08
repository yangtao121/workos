package chromiumengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// cdpConn is a minimal Chrome DevTools Protocol JSON-RPC client over the
// HTTP endpoints: navigate via the page target's ws is avoided by using the
// /json/new and /json/navigate HTTP helpers plus the page ws for
// screenshots. The HTTP surface keeps the client dependency-free and
// bounded; only the fixed command set is exposed.
type cdpConn struct {
	closed atomic.Bool
	base   string
	http   *http.Client
	// opMu serializes whole operations (navigate, capture): the websocket
	// has exactly one reader at a time, so interleaved operations would
	// consume each other's responses.
	opMu   sync.Mutex
	mu     sync.Mutex
	target string
	nextID atomic.Int64
	ws     *wsConn
}

var errCDP = errors.New("browser devtools call failed")

func newCDP(devtoolsURL string) *cdpConn {
	// The DevTools endpoint is advertised as a websocket URL with a browser
	// target path; the HTTP command surface is the scheme and host only.
	parsed, err := url.Parse(devtoolsURL)
	if err != nil || parsed.Host == "" {
		return &cdpConn{base: "", http: &http.Client{Timeout: 10 * time.Second}}
	}
	return &cdpConn{base: "http://" + parsed.Host, http: &http.Client{Timeout: 10 * time.Second}}
}

// openPage connects to the page target once; navigations reuse the same
// target so the compositor and websocket stay stable across captures.
func (c *cdpConn) openPage(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws != nil {
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/json/new?about:blank", nil)
	if err != nil {
		return errCDP
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: new page: %v", errCDP, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return errCDP
	}
	var target struct {
		WebsocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		ID                   string `json:"id"`
	}
	if json.Unmarshal(body, &target) != nil || target.WebsocketDebuggerURL == "" {
		return errCDP
	}
	ws, err := dialWebsocket(ctx, target.WebsocketDebuggerURL)
	if err != nil {
		return fmt.Errorf("%w: connect page: %v", errCDP, err)
	}
	if c.ws != nil {
		c.ws.close()
	}
	c.ws = ws
	c.target = target.ID
	return nil
}

// call sends one CDP command and waits for its matching response.
func (c *cdpConn) call(ctx context.Context, method string, params map[string]any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil {
		return errCDP
	}
	id := c.nextID.Add(1)
	payload := map[string]any{"id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.ws.send(payload); err != nil {
		return fmt.Errorf("%w: send %s: %v", errCDP, method, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		message, err := c.ws.receive(ctx)
		if err != nil {
			c.closed.Store(true)
			return fmt.Errorf("%w: receive %s: %v", errCDP, method, err)
		}
		var envelope struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(message, &envelope) != nil || envelope.ID != id {
			continue
		}
		if envelope.Error != nil {
			return fmt.Errorf("%w: %s: %s", errCDP, method, envelope.Error.Message)
		}
		if result != nil && len(envelope.Result) > 0 {
			return json.Unmarshal(envelope.Result, result)
		}
		return nil
	}
	return fmt.Errorf("%w: %s timed out", errCDP, method)
}

// Navigate drives the page to an http(s) URL and waits for the page load
// event on the same websocket, so the next capture sees a settled page.
func (c *cdpConn) Navigate(ctx context.Context, url string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.call(ctx, "Page.enable", nil, nil); err != nil {
		return err
	}
	if err := c.call(ctx, "Page.navigate", map[string]any{"url": url}, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		message, err := c.ws.receive(ctx)
		if err != nil {
			// The socket closed mid-navigation: report honestly; the caller
			// bounded-retries or restarts the worker.
			return fmt.Errorf("%w: wait load: %v", errCDP, err)
		}
		var event struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(message, &event) != nil {
			continue
		}
		if event.Method == "Page.loadEventFired" {
			return nil
		}
	}
	return nil
}

// Screenshot captures one bounded JPEG frame of the current page.
func (c *cdpConn) Screenshot(ctx context.Context) ([]byte, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	var result struct {
		Data string `json:"data"`
	}
	if err := c.call(ctx, "Page.captureScreenshot", map[string]any{
		"format": "jpeg", "quality": 70,
		"clip": map[string]any{"x": 0, "y": 0, "width": 1280, "height": 800, "scale": 1},
	}, &result); err != nil {
		return nil, err
	}
	return decodeBase64Bytes(result.Data)
}

// Dead reports whether the page websocket has failed: the transport-level
// worker-death signal, independent of process reaping.
func (c *cdpConn) Dead() bool { return c.closed.Load() }

// Close terminates the page websocket.
func (c *cdpConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws != nil {
		c.ws.close()
		c.ws = nil
	}
}
