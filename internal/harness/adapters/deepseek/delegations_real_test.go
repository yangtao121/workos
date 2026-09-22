package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

func TestNativeTwoForegroundChildrenShareScopeAndBudget(t *testing.T) {
	runtime := os.Getenv("WORKOS_DEEPSEEK_RUNTIME_PROBE")
	if runtime == "" {
		t.Skip("set WORKOS_DEEPSEEK_RUNTIME_PROBE to the pinned runtime")
	}
	plugin, err := filepath.Abs("../../../../deploy/harness/workos-tools.mjs")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	budgets := []int64{}
	childrenStarted := 0
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		var request struct {
			MaxTokens int64 `json:"max_tokens"`
			Tools     []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		mu.Lock()
		budgets = append(budgets, request.MaxTokens)
		mu.Unlock()
		child := ""
		tools := 0
		for _, message := range request.Messages {
			if message.Role == "user" && (message.Content == "CHILD_A" || message.Content == "CHILD_B") {
				child = message.Content
			}
			if message.Role == "tool" {
				tools++
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta map[string]any, reason string) {
			value := map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2}}
			encoded, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
		}
		tool := func(index int, name, id string, args map[string]any) any {
			value, _ := json.Marshal(args)
			return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(value)}}
		}
		if child != "" {
			for _, tool := range request.Tools {
				switch tool.Function.Name {
				case "subagent", "create_goal", "update_goal", "ask_user_question":
					t.Errorf("child gained forbidden tool %s", tool.Function.Name)
				}
			}
			if strings.Contains(string(body), "PARENT_PRIVATE_CONTEXT") {
				t.Error("child inherited parent conversation")
			}
			if tools == 0 {
				mu.Lock()
				childrenStarted++
				if childrenStarted == 2 {
					close(both)
				}
				mu.Unlock()
				select {
				case <-both:
				case <-time.After(8 * time.Second):
					t.Error("foreground children did not overlap")
					http.Error(w, "no overlap", 400)
					return
				}
				emit(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{tool(0, "bash", "bash-"+child, map[string]any{"command": child, "description": "verify isolated child scope"})}}, "tool_calls")
			} else {
				emit(map[string]any{"role": "assistant", "content": child + " finished"}, "stop")
			}
			return
		}
		if tools == 0 {
			emit(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{tool(0, "subagent", "child-a", map[string]any{"description": "First fixture child", "prompt": "CHILD_A"}), tool(1, "subagent", "child-b", map[string]any{"description": "Second fixture child", "prompt": "CHILD_B"})}}, "tool_calls")
		} else {
			emit(map[string]any{"role": "assistant", "content": "Both children finished"}, "stop")
		}
	}))
	defer server.Close()
	manager := NewSessionManager(normalizeConfig(Config{Enabled: true, Environment: "test", RuntimePath: runtime, BaseURL: server.URL, Model: "deepseek-v4-flash", WorkosToolsPath: plugin, Timeout: 30 * time.Second}), nil)
	defer manager.Shutdown()
	root := t.TempDir()
	if override := os.Getenv("WORKOS_NATIVE_CHILD_PROBE_STATE"); override != "" {
		root = override
	}
	proc, err := manager.Ensure(context.Background(), "native-children", "", root, []byte("native-fixture-key"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	granted := map[string]string{}
	commands := map[string]string{}
	completed := map[string]bool{}
	proc.tools = func(ctx context.Context, op string, args map[string]any) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		scope := ports.ToolDelegation(ctx)
		if scope != "" {
			if _, ok := granted[scope]; !ok {
				return nil, fmt.Errorf("unknown delegation")
			}
		}
		switch op {
		case "delegation.acquire":
			if scope != "" || len(granted) >= 2 {
				return nil, fmt.Errorf("delegation limit")
			}
			id := (ids.UUIDv7{}).New()
			granted[id], _ = args["title"].(string)
			return map[string]any{"delegationId": id, "worktreeId": id, "state": "running", "baseCommit": strings.Repeat("a", 40)}, nil
		case "delegation.finish":
			if scope == "" || args["state"] != "completed" || commands[scope] == "" {
				return nil, fmt.Errorf("invalid completion")
			}
			completed[scope] = true
			return map[string]any{"state": "completed"}, nil
		case "shell.run":
			if scope == "" {
				return nil, fmt.Errorf("child tool used parent scope")
			}
			command, _ := args["command"].(string)
			commands[scope] = command
			return map[string]any{"exitCode": 0, "stdout": map[string]any{"text": command, "truncated": false, "totalBytes": len(command)}, "stderr": map[string]any{"text": "", "truncated": false, "totalBytes": 0}, "timedOut": false, "aborted": false}, nil
		case "fs.resolve":
			return map[string]any{"displayPath": args["path"]}, nil
		case "fs.stat":
			return map[string]any{"absent": true}, nil
		case "fs.list":
			return map[string]any{"entries": []any{}}, nil
		default:
			return nil, fmt.Errorf("unexpected fixture operation %s", op)
		}
	}
	var usage int64
	childEvents := map[string]bool{}
	emit := func(event *agentv1.AgentEvent) error {
		if event.DelegationId != "" {
			childEvents[event.DelegationId] = true
		}
		if value := event.GetUsageRecorded(); value != nil {
			usage += value.OutputTokens
		}
		return nil
	}
	if err := manager.Prompt(context.Background(), proc, "two-children", "PARENT_PRIVATE_CONTEXT: start the two children", 40, 25*time.Second, emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(granted) != 2 || len(completed) != 2 || len(childEvents) != 2 || usage != 12 {
		t.Fatalf("grants=%d complete=%d events=%d usage=%d budgets=%v", len(granted), len(completed), len(childEvents), usage, budgets)
	}
	if len(budgets) != 6 || budgets[0] != 40 || budgets[1]+budgets[2] > 38 || budgets[5] != 30 {
		t.Fatalf("concurrent reservations or final budget incorrect: %v", budgets)
	}
	if len(commands) != 2 {
		t.Fatal("child scopes shared")
	}
}
