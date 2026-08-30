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
		body, _ := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
		goal, err := validate(request, key, body)
		if err != nil {
			http.Error(response, "invalid fixture request", http.StatusBadRequest)
			return
		}
		// Structured review mode: when the request carries the versioned
		// output contract, the model answer is exactly one strict JSON
		// review document (or one of the deterministic malformed variants).
		if contract := extractOutputContract(body, key); contract != nil {
			switch goal {
			case "fixture malformed output":
				// A cleanly terminated stream whose answer is a truncated
				// JSON document: the adapter's strict parser must fail the
				// run as a deterministic protocol failure.
				writeSSE(response, []string{
					`{"choices":[{"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
					`{"choices":[{"delta":{"content":"{\"version\":\"workos.deepseek.review-output.v1\",\"summary\":\"partial"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`,
					`[DONE]`,
				})
				return
			case "fixture extra output":
				writeSSE(response, []string{`{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# x\n","code.unified-diff.v1":"diff\n","image.v1":"zz"}}}`, "[DONE]"})
				return
			case "fixture missing output":
				writeSSE(response, []string{`{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# x\n"}}`})
				return
			case "fixture oversize output":
				big := strings.Repeat("x", 600*1024)
				writeSSE(response, []string{`{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"` + big + `\n"}}`})
				return
			case "fixture invalid output":
				writeSSE(response, []string{`{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"line1\u0000line2\n"}}`})
				return
			default:
				markdown := "# DeepSeek Review Document\n\nsynthetic structured review body\n"
				patch := "diff --git a/src/example.ts b/src/example.ts\n--- a/src/example.ts\n+++ b/src/example.ts\n@@ -1,2 +1,3 @@\n const a = 1;\n+b\n"
				response.Header().Set("Content-Type", "text/event-stream")
				response.Header().Set("Cache-Control", "no-cache")
				flusher, _ := response.(http.Flusher)
				summary := "structured review completed"
				if contract.artifacts == 1 {
					summary += " with one artifact"
				} else {
					summary += " with two artifacts"
				}
				payload := fmt.Sprintf(`{"version":"workos.deepseek.review-output.v1","summary":%q,"artifacts":{`, summary)
				if contract.hasType("document.markdown.v1") {
					payload += `"document.markdown.v1":` + jsonString(markdown)
				}
				if contract.hasType("code.unified-diff.v1") {
					if contract.hasType("document.markdown.v1") {
						payload += ","
					}
					payload += `"code.unified-diff.v1":` + jsonString(patch)
				}
				payload += "}}"
				for _, event := range []string{
					`{"choices":[{"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
					`{"choices":[{"delta":{"content":` + jsonString(payload) + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"prompt_cache_hit_tokens":2,"completion_tokens":3}}`,
					`[DONE]`,
				} {
					_, _ = fmt.Fprintf(response, "data: %s\n\n", event)
					if flusher != nil {
						flusher.Flush()
					}
				}
				return
			}
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

func validate(request *http.Request, key string, body []byte) (string, error) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+key {
		return "", errors.New("invalid request metadata")
	}
	if contentType := request.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return "", errors.New("invalid content type")
	}
	if len(body) > maximumRequestBytes || strings.Contains(string(body), key) {
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
		"fixture rate limit", "fixture server unavailable", "fixture malformed SSE", "fixture early EOF",
		"fixture unexpected content type", "review the pinned context", "review and propose changes",
		"fixture malformed output", "fixture extra output", "fixture missing output",
		"fixture oversize output", "fixture invalid output", "produce structured review":
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

// outputContractInfo mirrors only the contract facts the fixture needs.
type outputContractInfo struct {
	required  []string
	artifacts int
}

func (c *outputContractInfo) hasType(artifactType string) bool {
	for _, value := range c.required {
		if value == artifactType {
			return true
		}
	}
	return false
}

func writeSSE(response http.ResponseWriter, events []string) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	for _, event := range events {
		_, _ = fmt.Fprintf(response, "data: %s\n\n", event)
	}
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// extractOutputContract inspects the already-buffered body for the versioned
// task envelope's output contract. It never logs or echoes request content.
func extractOutputContract(body []byte, key string) *outputContractInfo {
	if len(body) == 0 || len(body) > maximumRequestBytes || strings.Contains(string(body), key) {
		return nil
	}
	// The envelope travels as a JSON-encoded string inside the request, so
	// quotes are escaped in the raw body: match marker substrings that do
	// not depend on quote characters.
	text := string(body)
	start := strings.Index(text, "output_contract")
	if start < 0 {
		return nil
	}
	info := &outputContractInfo{}
	windowEnd := func(from int) int {
		end := from + 200
		if end > len(text) {
			return len(text)
		}
		return end
	}
	for _, artifactType := range []string{"document.markdown.v1", "code.unified-diff.v1"} {
		marker := strings.Index(text[start:], "required_artifacts")
		if marker < 0 {
			continue
		}
		from := start + marker
		if strings.Contains(text[from:windowEnd(from)], artifactType) {
			info.required = append(info.required, artifactType)
		}
	}
	info.artifacts = len(info.required)
	if len(info.required) == 0 {
		return nil
	}
	return info
}

func writeAPIError(response http.ResponseWriter, status int) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, `{"error":{"message":"fixture provider error","type":"fixture_error","code":"fixture_error"}}`)
}
