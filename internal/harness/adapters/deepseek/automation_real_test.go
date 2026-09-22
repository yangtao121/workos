package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
)

// This probe loads the pinned official goal service and round driver. Only the
// model endpoint and empty project filesystem are fixtures; the scheduler,
// persistence, tool registry and agent loop are the real native binary.
func TestNativeGoalRoundsPauseResumeAndBudget(t *testing.T) {
	runtime := os.Getenv("WORKOS_DEEPSEEK_RUNTIME_PROBE")
	if runtime == "" {
		t.Skip("set WORKOS_DEEPSEEK_RUNTIME_PROBE to the pinned runtime")
	}
	plugin, err := filepath.Abs("../../../../deploy/harness/workos-tools.mjs")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var budgets []int64
	var missingUsage atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		if r.URL.Path != "/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil || request.MaxTokens <= 0 {
			http.Error(w, "bad fixture request", 400)
			return
		}
		mu.Lock()
		budgets = append(budgets, request.MaxTokens)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if missingUsage.Load() {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"No usage fixture\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Native round finished\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	config := normalizeConfig(Config{Enabled: true, Environment: "test", RuntimePath: runtime, BaseURL: server.URL, Model: "deepseek-v4-flash", WorkosToolsPath: plugin, Timeout: 30 * time.Second})
	manager := NewSessionManager(config, nil)
	defer manager.Shutdown()
	root := t.TempDir()
	if override := os.Getenv("WORKOS_NATIVE_GOAL_PROBE_STATE"); override != "" {
		root = override
	}
	var goal *agentv1.SessionGoal
	pauseAfterFirst := true
	backend := func(_ context.Context, operation string, args map[string]any) (map[string]any, error) {
		switch operation {
		case "session.control":
			mu.Lock()
			defer mu.Unlock()
			ref := ""
			if pauseAfterFirst && goal != nil && goal.RoundsStarted == 1 {
				ref = goal.Ref
			}
			return map[string]any{"pauseGoalRef": ref}, nil
		case "fs.resolve":
			return map[string]any{"displayPath": args["path"]}, nil
		case "fs.stat":
			return map[string]any{"absent": true}, nil
		case "fs.list":
			return map[string]any{"entries": []any{}}, nil
		default:
			return nil, fmt.Errorf("unexpected operation %s", operation)
		}
	}
	var used int64
	emit := func(event *agentv1.AgentEvent) error {
		if update := event.GetGoalUpdated(); update != nil {
			mu.Lock()
			goal = update.Goal
			mu.Unlock()
		}
		if usage := event.GetUsageRecorded(); usage != nil {
			used += usage.OutputTokens
		}
		return nil
	}
	ensure := func(id string) *sessionProcess {
		t.Helper()
		proc, err := manager.Ensure(context.Background(), id, "", root, []byte("native-fixture-key"), "", "")
		if err != nil {
			t.Fatal(err)
		}
		proc.tools = backend
		return proc
	}
	proc := ensure("native-goal")
	directive := &agentv1.SessionDirective{Kind: agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_CREATE_GOAL, Objective: "Finish three fixture rounds", MaxRounds: 3}
	if err := manager.prompt(context.Background(), proc, "create", "", 20, 20*time.Second, directive, emit); err != nil {
		t.Fatal(err)
	}
	if goal == nil || goal.Phase != "paused" || goal.Armed || goal.RoundsStarted != 1 || used != 2 {
		t.Fatalf("pause projection=%+v used=%d", goal, used)
	}
	manager.Close("native-goal")
	pauseAfterFirst = false
	proc = ensure("native-goal")
	directive = &agentv1.SessionDirective{Kind: agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_RESUME_GOAL, GoalRef: goal.Ref, ExpectedRevision: goal.Revision}
	if err := manager.prompt(context.Background(), proc, "resume", "", 20, 20*time.Second, directive, emit); err != nil {
		t.Fatal(err)
	}
	if goal.Phase != "blocked" || goal.Armed || goal.RoundsStarted != 3 || used != 6 {
		t.Fatalf("resumed projection=%+v used=%d", goal, used)
	}
	mu.Lock()
	actual := append([]int64(nil), budgets...)
	mu.Unlock()
	if fmt.Sprint(actual) != "[20 20 18]" {
		t.Fatalf("cumulative request budgets=%v", actual)
	}
	manager.Close("native-goal")
	goal = nil
	proc = ensure("native-budget")
	directive = &agentv1.SessionDirective{Kind: agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_CREATE_GOAL, Objective: "Exhaust fixture budget", MaxRounds: 3}
	if err := manager.prompt(context.Background(), proc, "budget", "", 4, 20*time.Second, directive, emit); err == nil {
		t.Fatal("automatic rounds exceeded shared task budget")
	}
	mu.Lock()
	actual = append([]int64(nil), budgets...)
	mu.Unlock()
	if fmt.Sprint(actual) != "[20 20 18 4 2]" {
		t.Fatalf("request after budget exhaustion: %v", actual)
	}
	manager.Close("native-budget")
	missingUsage.Store(true)
	proc = ensure("native-unknown-usage")
	if err := manager.prompt(context.Background(), proc, "unknown-usage", "", 20, 20*time.Second, directive, emit); err == nil {
		t.Fatal("missing usage accepted")
	}
	mu.Lock()
	actual = append([]int64(nil), budgets...)
	mu.Unlock()
	if len(actual) != 6 {
		t.Fatalf("unknown paid request was retried: %v", actual)
	}

}
