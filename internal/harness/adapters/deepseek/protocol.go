package deepseek

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const jsonRPCVersion = "2.0"

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type initializeResult struct {
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type promptResult struct {
	MessageID string `json:"messageId"`
}

type sessionEventParams struct {
	SessionID string `json:"sessionId"`
	Event     struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"event"`
}

type sessionStatusParams struct {
	SessionID string          `json:"sessionId"`
	Status    json.RawMessage `json:"status"`
}

type tokenUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

type turnEndReason struct {
	Kind  string          `json:"kind"`
	Error json.RawMessage `json:"error,omitempty"`
}

type llmFailure struct {
	Code                 string `json:"code"`
	Status               int    `json:"status"`
	ProviderRetryAfterMS int64  `json:"providerRetryAfterMs"`
}

func decodeEnvelope(line []byte) (rpcEnvelope, error) {
	var envelope rpcEnvelope
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return rpcEnvelope{}, errors.New("DeepSeek Harness emitted malformed JSON-RPC")
	}
	if envelope.JSONRPC != jsonRPCVersion {
		return rpcEnvelope{}, errors.New("DeepSeek Harness emitted an unsupported JSON-RPC version")
	}
	if len(envelope.ID) == 0 && envelope.Method == "" {
		return rpcEnvelope{}, errors.New("DeepSeek Harness emitted an empty JSON-RPC envelope")
	}
	return envelope, nil
}

func responseID(envelope rpcEnvelope) (int64, error) {
	var id int64
	if len(envelope.ID) == 0 || string(envelope.ID) == "null" {
		return 0, errors.New("DeepSeek Harness response omitted its request ID")
	}
	if err := json.Unmarshal(envelope.ID, &id); err != nil {
		return 0, errors.New("DeepSeek Harness returned a non-numeric request ID")
	}
	return id, nil
}

func decodeStatus(raw json.RawMessage) (string, error) {
	var status string
	if err := json.Unmarshal(raw, &status); err == nil && status != "" {
		return status, nil
	}
	var value struct {
		Status string `json:"status"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("DeepSeek Harness emitted a malformed session status")
	}
	if value.Status != "" {
		return value.Status, nil
	}
	if value.State != "" {
		return value.State, nil
	}
	return "", errors.New("DeepSeek Harness emitted an empty session status")
}

func decodeFailure(raw json.RawMessage) llmFailure {
	var direct llmFailure
	_ = json.Unmarshal(raw, &direct)
	if direct.Code != "" || direct.Status != 0 {
		return direct
	}
	var nested struct {
		Failure llmFailure `json:"failure"`
		Error   llmFailure `json:"error"`
	}
	_ = json.Unmarshal(raw, &nested)
	if nested.Failure.Code != "" || nested.Failure.Status != 0 {
		return nested.Failure
	}
	return nested.Error
}

func marshalRequest(id int64, method string, params any) ([]byte, error) {
	value, err := json.Marshal(rpcRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode DeepSeek Harness request: %w", err)
	}
	return append(value, '\n'), nil
}
