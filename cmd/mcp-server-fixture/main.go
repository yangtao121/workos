// Command mcp-server-fixture is the versioned local MCP stdio server
// stand-in used by every test and gate for the MCP harness adapter
// (ADR-0015). It speaks the MCP-shaped JSON-RPC 2.0 line protocol the
// adapter implements — initialize, tools/list, tools/call — over stdio with
// zero network access, deterministic outputs, and no real credentials.
// Behavior modes are selected by MCP_FIXTURE_MODE and exist only to prove
// the adapter's failure matrix.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// protocolVersion pins the fixture contract. The adapter must negotiate it
// explicitly during initialize; anything else fails the run.
const protocolVersion = "workos.mcp-server/v1"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		ProtocolVersion string `json:"protocolVersion"`
		Arguments       struct {
			Goal string `json:"goal"`
		} `json:"arguments"`
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

func main() {
	mode := strings.TrimSpace(os.Getenv("MCP_FIXTURE_MODE"))
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 64*1024), 8*1024*1024)
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
			if mode == "protocol-error" {
				emit(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "initialize refused"}})
				continue
			}
			if req.Params.ProtocolVersion != protocolVersion {
				reply(req.ID, map[string]any{"error": "unsupported protocol version"})
				continue
			}
			reply(req.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]string{"name": "mcp-server-fixture", "version": "1.0.0"},
			})
		case "tools/list":
			tools := []map[string]any{{
				"name":        "task",
				"description": "deterministic fixture task tool",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"goal": map[string]any{"type": "string"}}},
			}}
			if mode == "no-tool" {
				tools = []map[string]any{}
			}
			reply(req.ID, map[string]any{"tools": tools})
		case "tools/call":
			switch mode {
			case "hang":
				// Response loss: never answers the call.
				time.Sleep(10 * time.Minute)
			case "crash":
				// Mid-call process death.
				os.Exit(7)
			case "oversize":
				// One bounded-protocol violation: a single line far beyond
				// the adapter's event budget.
				fmt.Println(`{"jsonrpc":"2.0","id":9,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 9*1024*1024) + `"}]}}`)
				return
			case "tool-error":
				emit(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: "tool execution failed"}})
				return
			default:
				goal := req.Params.Arguments.Goal
				reply(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "mcp fixture result for: " + goal}},
				})
				return
			}
		default:
			emit(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "unknown method"}})
		}
	}
	if err := reader.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "fixture stdin failure")
		os.Exit(1)
	}
}
