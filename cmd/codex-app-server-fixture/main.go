// Command codex-app-server-fixture is the versioned local Codex App Server
// stand-in used by every test and gate for the Codex harness adapter
// (ADR-0015). It speaks the same line-delimited JSON-RPC 2.0 protocol the
// adapter implements against the real app server contract — over stdio, with
// zero network access and no real credentials — and is deterministic in its
// outputs. Behavior modes are selected by CODEX_FIXTURE_MODE and exist only
// to prove the adapter's failure matrix.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// protocolVersion pins the fixture contract. Bumping it is a versioned
// protocol change that the adapter must negotiate explicitly.
const protocolVersion = "workos.codex.app-server/v1"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		TaskID          string `json:"taskId"`
		Goal            string `json:"goal"`
		MaxTokens       int64  `json:"maxTokens"`
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type event struct {
	Kind         string `json:"kind"` // started|delta|message|usage|completed|failed
	RunID        string `json:"runId"`
	Seq          int    `json:"seq"`
	Text         string `json:"text,omitempty"`
	Delta        string `json:"delta,omitempty"`
	Provider     string `json:"provider,omitempty"`
	OutputTokens int64  `json:"outputTokens,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func main() {
	mode := strings.TrimSpace(os.Getenv("CODEX_FIXTURE_MODE"))
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 64*1024), 1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	emit := func(value any) {
		encoded, _ := json.Marshal(value)
		writer.Write(encoded)
		writer.WriteByte('\n')
		writer.Flush()
	}
	reply := func(id json.RawMessage, result any) {
		emit(response{JSONRPC: "2.0", ID: id, Result: result})
	}
	var runID string
	var seq int
	writeEvent := func(kind, text string, tokens int64) {
		seq++
		// Notifications wrap the protocol event: params.event carries the
		// typed fact (workos.codex.app-server/v1).
		emit(map[string]any{
			"jsonrpc": "2.0", "method": "task/event",
			"params": map[string]any{
				"event": event{Kind: kind, RunID: runID, Seq: seq, Text: text, Delta: text, OutputTokens: tokens, Provider: "codex-fixture"},
			},
		})
	}
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			emit(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		switch req.Method {
		case "initialize":
			if req.Params.ProtocolVersion != protocolVersion {
				reply(req.ID, map[string]any{"error": "unsupported protocol version"})
				continue
			}
			reply(req.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]string{"name": "codex-app-server-fixture", "version": "1.0.0"},
				"capabilities":    map[string]any{"streaming": true, "usageReporting": true},
			})
		case "task/new":
			if mode == "crash" {
				// Mid-run process death: the adapter must converge the task
				// to a deterministic terminal without duplicating usage.
				os.Exit(9)
			}
			runID = "fx-" + req.Params.TaskID
			reply(req.ID, map[string]any{"runId": runID})
			switch mode {
			case "silent":
				// Response loss hang: never emits another byte.
				time.Sleep(time.Hour)
			case "out-of-order":
				// Malformed stream: terminal before anything else, then a
				// message after the terminal. The adapter must reject.
				writeEvent("completed", "", 0)
				writeEvent("message", "late", 0)
				return
			case "over-budget":
				// The fixture enforces the caller's max_tokens cap exactly
				// like the pinned real runtime: deltas stop at the cap and
				// reported usage never exceeds it.
				budget := req.Params.MaxTokens
				if budget <= 0 || budget > 4096 {
					budget = 4096
				}
				writeEvent("started", "", 0)
				emitted := int64(0)
				for emitted < budget {
					chunk := "x"
					if emitted+1 > budget {
						break
					}
					writeEvent("delta", chunk, 0)
					emitted++
				}
				writeEvent("usage", "", emitted)
				writeEvent("completed", "", 0)
				return
			case "slow":
				// Ignores cooperative cancellation and sleeps far beyond any
				// sane runtime deadline; the adapter's hard process deadline
				// is what terminates it.
				writeEvent("started", "", 0)
				time.Sleep(10 * time.Minute)
				return
			default:
				writeEvent("started", "", 0)
				writeEvent("delta", "Analyzing: ", 0)
				writeEvent("delta", req.Params.Goal, 0)
				writeEvent("message", "codex fixture reviewed: "+req.Params.Goal, 0)
				writeEvent("usage", "", int64(12+len(req.Params.Goal)%7))
				writeEvent("completed", "", 0)
				return
			}
		case "task/cancel":
			reply(req.ID, map[string]any{"cancelled": runID})
		default:
			emit(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "unknown method"}})
		}
	}
	if err := reader.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "fixture stdin failure")
		os.Exit(1)
	}
}
