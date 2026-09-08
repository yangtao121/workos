package chromiumengine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// wsConn wraps the CDP page websocket with bounded sends and receives.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func dialWebsocket(ctx context.Context, url string) (*wsConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		return nil, err
	}
	// Screencast responses exceed the library's 32 KiB default; keep our own
	// bounded budget (4 MiB) enforced in receive.
	conn.SetReadLimit(4 << 20)
	return &wsConn{conn: conn}, nil
}

func (w *wsConn) send(payload any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.conn.Write(ctx, websocket.MessageText, mustJSON(payload))
}

func (w *wsConn) receive(ctx context.Context) (json.RawMessage, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	kind, data, err := w.conn.Read(readCtx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageText {
		return nil, errors.New("unexpected binary devtools frame")
	}
	if len(data) > 4<<20 {
		return nil, errors.New("devtools frame exceeds budget")
	}
	return json.RawMessage(data), nil
}

func (w *wsConn) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.CloseNow()
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func decodeBase64Bytes(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("empty screenshot payload")
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(value)))
	count, err := base64.StdEncoding.Decode(decoded, []byte(value))
	if err != nil {
		return nil, err
	}
	return decoded[:count], nil
}
