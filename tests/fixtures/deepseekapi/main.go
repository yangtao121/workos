// Command deepseekapi is a local, keyless stand-in for the official DeepSeek
// streaming endpoint. It validates the request boundary without retaining or
// logging prompts, credentials, headers, or response bodies.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

const maximumRequestBytes = 5 * 1024 * 1024

type chatRequest struct {
	Model         string `json:"model"`
	MaxTokens     int64  `json:"max_tokens"`
	Stream        bool   `json:"stream"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
}

func main() {
	address := os.Getenv("WORKOS_DEEPSEEK_FIXTURE_ADDRESS")
	if address == "" {
		address = "127.0.0.1:18086"
	}
	key := os.Getenv("WORKOS_DEEPSEEK_FIXTURE_KEY")
	if key == "" {
		log.Fatal("fixture key is required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/chat/completions", func(response http.ResponseWriter, request *http.Request) {
		goal, err := validate(request, key)
		if err != nil {
			http.Error(response, "invalid fixture request", http.StatusBadRequest)
			return
		}
		switch goal {
		case "fixture rate limit":
			writeAPIError(response, http.StatusTooManyRequests)
			return
		case "fixture server unavailable":
			writeAPIError(response, http.StatusServiceUnavailable)
			return
		case "fixture malformed SSE":
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "data: {not-json}\n\ndata: [DONE]\n\n")
			return
		case "fixture early EOF":
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			return
		case "fixture unexpected content type":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{}`)
			return
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		flusher, _ := response.(http.Flusher)
		for _, event := range []string{
			`{"choices":[{"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
			`{"choices":[{"delta":{"content":"fixture "}}]}`,
			`{"choices":[{"delta":{"content":"response"}}]}`,
			`{"choices":[{"delta":{"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`,
			`[DONE]`,
		} {
			_, _ = fmt.Fprintf(response, "data: %s\n\n", event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	log.Printf("DeepSeek API fixture listening on %s", address)
	log.Fatal(http.ListenAndServe(address, mux))
}

func validate(request *http.Request, key string) (string, error) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+key {
		return "", errors.New("invalid request metadata")
	}
	if contentType := request.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return "", errors.New("invalid content type")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || len(body) > maximumRequestBytes || strings.Contains(string(body), key) {
		return "", errors.New("invalid request body")
	}
	var value chatRequest
	if err := json.Unmarshal(body, &value); err != nil {
		return "", errors.New("malformed request body")
	}
	if value.Model != "deepseek-v4-flash" || (value.MaxTokens != 64 && value.MaxTokens != 2048 && value.MaxTokens != 8192) || !value.Stream || !value.StreamOptions.IncludeUsage || len(value.Messages) == 0 {
		return "", errors.New("unexpected request mapping")
	}
	last := value.Messages[len(value.Messages)-1]
	if last.Role != "user" {
		return "", errors.New("goal was not mapped to a user message")
	}
	text, ok := last.Content.(string)
	if !ok {
		return "", errors.New("unexpected user goal")
	}
	// The adapter may wrap the goal in the versioned task envelope when the
	// task carries pinned context (ADR-0010). The envelope is unwrapped and
	// its inner goal must still match the allowlist; context entries are
	// validated for shape and bound but never logged or echoed.
	if envelope := decodeEnvelope(text); envelope != nil {
		if envelope.Version != "workos.deepseek.task-envelope.v1" {
			return "", errors.New("unexpected envelope version")
		}
		text = envelope.Goal
		for _, context := range envelope.UntrustedContexts {
			if context.RefType != "artifact.review.v1" || context.Digest == "" || len(context.BytesBase64) > 700*1024 {
				return "", errors.New("unexpected context entry")
			}
		}
	}
	switch text {
	case "prove the DeepSeek project binding fixture", "persist this completed run across service restart",
		"fixture rate limit", "fixture server unavailable", "fixture malformed SSE", "fixture early EOF", "fixture unexpected content type":
		return text, nil
	default:
		return "", errors.New("unexpected user goal")
	}
}

// taskEnvelope mirrors only the facts the fixture validates.
type taskEnvelope struct {
	Version           string `json:"version"`
	Goal              string `json:"goal"`
	UntrustedContexts []struct {
		RefType      string `json:"refType"`
		ArtifactType string `json:"artifactType"`
		Digest       string `json:"digest"`
		BytesBase64  string `json:"bytesBase64"`
	} `json:"untrusted_contexts"`
}

func decodeEnvelope(text string) *taskEnvelope {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var envelope taskEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.Version == "" {
		return nil
	}
	return &envelope
}

func writeAPIError(response http.ResponseWriter, status int) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, `{"error":{"message":"fixture provider error","type":"fixture_error","code":"fixture_error"}}`)
}
