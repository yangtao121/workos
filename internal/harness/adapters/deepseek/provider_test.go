package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

type fixedID string

func (id fixedID) New() string { return string(id) }

func TestConfigAndDescribe(t *testing.T) {
	t.Run("disabled by default even with key", func(t *testing.T) {
		provider := New(Config{APIKey: "not-a-real-key"}, fixedID("run-1"))
		description := provider.Describe()
		if description.GetId() != ProviderID || description.GetAdapterVersion() != AdapterVersion {
			t.Fatalf("unexpected provider identity: %#v", description)
		}
		if description.GetHealth() != commonv1.HealthState_HEALTH_STATE_UNAVAILABLE || !strings.Contains(description.GetUnavailableReason(), "disabled") {
			t.Fatalf("disabled provider reported unexpected health: %#v", description)
		}
		caps := description.GetCapabilities()
		if !caps.GetStreaming() || !caps.GetUsageReporting() || caps.GetPersistentSessions() || caps.GetResume() || caps.GetSteerDuringRun() || caps.GetApprovals() || caps.GetToolRegistration() || caps.GetMcp() || caps.GetSubagents() || caps.GetWorkspaceMount() || caps.GetStructuredArtifacts() {
			t.Fatalf("provider overclaimed capabilities: %#v", caps)
		}
	})

	t.Run("explicit valid configuration", func(t *testing.T) {
		provider := New(validConfig(t, "success"), fixedID("run-1"))
		if got := provider.Describe(); got.GetHealth() != commonv1.HealthState_HEALTH_STATE_HEALTHY || got.GetUnavailableReason() != "" {
			t.Fatalf("valid provider reported unexpected health: %#v", got)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"missing key", func(c *Config) { c.APIKey = "" }, "not configured"},
		{"invalid key", func(c *Config) { c.APIKey = "value\nheader" }, "key is invalid"},
		{"invalid URL", func(c *Config) { c.BaseURL = "://bad" }, "base URL is invalid"},
		{"HTTP in production", func(c *Config) { c.Environment = "production" }, "must use HTTPS"},
		{"invalid timeout", func(c *Config) { c.Timeout = 500 * time.Millisecond }, "between 1s and 10m"},
		{"unsupported model", func(c *Config) { c.Model = "invented-model" }, "not supported"},
		{"loader issue", func(c *Config) { c.ConfigurationIssue = "raw parse detail" }, "invalid value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig(t, "success")
			test.mutate(&config)
			provider := New(config, fixedID("run-1"))
			got := provider.Describe()
			if got.GetHealth() != commonv1.HealthState_HEALTH_STATE_UNAVAILABLE || !strings.Contains(got.GetUnavailableReason(), test.want) {
				t.Fatalf("unexpected unavailable description: %#v", got)
			}
		})
	}
}

func TestInputPolicyAndBudgets(t *testing.T) {
	base := &agentv1.AgentTaskInput{Goal: "hello"}
	if prepared, err := prepareInput(base, DefaultTimeout); err != nil || prepared.maxTokens != DefaultMaxTokens || prepared.timeout != DefaultTimeout {
		t.Fatalf("unexpected defaults: %#v, %v", prepared, err)
	}
	custom := &agentv1.AgentTaskInput{Goal: "hello", Role: "general", Budget: &agentv1.AgentBudget{MaxTokens: 1234, MaxRuntimeSeconds: 7}}
	if prepared, err := prepareInput(custom, DefaultTimeout); err != nil || prepared.maxTokens != 1234 || prepared.timeout != 7*time.Second {
		t.Fatalf("unexpected budget mapping: %#v, %v", prepared, err)
	}

	tests := map[string]*agentv1.AgentTaskInput{
		"role":         {Goal: "hello", Role: "system"},
		"padded role":  {Goal: "hello", Role: " general "},
		"context":      {Goal: "hello", ContextRefs: []*agentv1.ContextRef{{Type: "artifact", Id: "one"}}},
		"capability":   {Goal: "hello", RequestedCapabilities: []string{"tools"}},
		"artifact":     {Goal: "hello", OutputArtifactTypes: []string{"report"}},
		"cost":         {Goal: "hello", Budget: &agentv1.AgentBudget{MaxCostDecimal: "1.00"}},
		"tokens":       {Goal: "hello", Budget: &agentv1.AgentBudget{MaxTokens: MaximumMaxTokens + 1}},
		"runtime":      {Goal: "hello", Budget: &agentv1.AgentBudget{MaxRuntimeSeconds: 601}},
		"missing goal": {},
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := prepareInput(input, DefaultTimeout)
			var runErr *ports.RunError
			if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindInvalidInput || runErr.Retryable {
				t.Fatalf("expected permanent invalid-input error, got %v", err)
			}
		})
	}
}

func TestProviderMapsOfficialRuntimeStreamInOrder(t *testing.T) {
	provider := New(validConfig(t, "success"), fixedID("0198d7ea-2110-7c42-b659-c5e4d73bc400"))
	var events []*agentv1.AgentEvent
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{
		Goal: "hello", Budget: &agentv1.AgentBudget{MaxTokens: 99, MaxRuntimeSeconds: 4},
	}, func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("unexpected event count: %d", len(events))
	}
	if got := events[0].GetRunStarted(); got.GetRunId() != "0198d7ea-2110-7c42-b659-c5e4d73bc400" || got.GetProviderId() != ProviderID {
		t.Fatalf("unexpected first event: %#v", events[0])
	}
	if events[1].GetAssistantDelta().GetText() != "Hel" || events[2].GetAssistantDelta().GetText() != "lo" {
		t.Fatalf("unexpected deltas: %#v", events)
	}
	if got := events[3].GetAssistantMessage().GetText(); got != "Hello" {
		t.Fatalf("unexpected assembled message: %q", got)
	}
	if got := events[4].GetUsageRecorded(); got.GetInputTokens() != 10 || got.GetOutputTokens() != 5 || got.GetCostDecimal() != "" || got.GetModel() != DefaultModel {
		t.Fatalf("unexpected usage: %#v", got)
	}
	if events[5].GetRunCompleted() == nil {
		t.Fatalf("last event was not RunCompleted: %#v", events[5])
	}
	terminals := 0
	for _, event := range events {
		if event.GetId() != "" || event.GetTaskId() != "" || event.GetSequence() != 0 || event.GetOccurredAt() != nil {
			t.Fatalf("provider set Core-owned metadata: %#v", event)
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("expected exactly one terminal event, got %d", terminals)
	}
}

func TestProviderStopsImmediatelyWhenEmitFails(t *testing.T) {
	provider := New(validConfig(t, "success"), fixedID("run-1"))
	want := errors.New("append failed")
	started := time.Now()
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(event *agentv1.AgentEvent) error {
		if event.GetAssistantDelta() != nil {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) || time.Since(started) > time.Second {
		t.Fatalf("emit failure was not returned promptly: %v", err)
	}
}

func TestProviderCancellationAndDeadlineStopRuntime(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		provider := New(validConfig(t, "hang"), fixedID("run-1"))
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- provider.Run(ctx, "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(*agentv1.AgentEvent) error { return nil })
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("runtime did not stop after cancellation")
		}
	})

	t.Run("deadline", func(t *testing.T) {
		config := validConfig(t, "hang")
		config.Timeout = time.Second
		provider := New(config, fixedID("run-1"))
		started := time.Now()
		err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(*agentv1.AgentEvent) error { return nil })
		var runErr *ports.RunError
		if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindTimeout || !runErr.Retryable || time.Since(started) > 2*time.Second {
			t.Fatalf("unexpected deadline result: %v", err)
		}
		if got := provider.Describe(); got.GetHealth() != commonv1.HealthState_HEALTH_STATE_DEGRADED {
			t.Fatalf("timeout did not mark provider temporarily unavailable: %#v", got)
		}
	})
}

func TestProviderRejectsUnsafeRuntimeOutput(t *testing.T) {
	for _, mode := range []string{"malformed", "unknown-event", "unexpected-content", "early-eof", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			provider := New(validConfig(t, mode), fixedID("run-1"))
			err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(*agentv1.AgentEvent) error { return nil })
			if err == nil {
				t.Fatal("expected runtime output to be rejected")
			}
			if strings.Contains(err.Error(), "not-a-real-key") {
				t.Fatal("error exposed the API key")
			}
		})
	}
}

func TestProviderClassifiesFailuresAndHealth(t *testing.T) {
	tests := []struct {
		name      string
		failure   llmFailure
		kind      ports.ErrorKind
		retryable bool
	}{
		{"401", llmFailure{Status: 401}, ports.ErrorKindAuthentication, false},
		{"403", llmFailure{Status: 403}, ports.ErrorKindAuthentication, false},
		{"429", llmFailure{Status: 429}, ports.ErrorKindRateLimit, true},
		{"server", llmFailure{Status: 503}, ports.ErrorKindProvider, true},
		{"transport", llmFailure{Code: "TRANSPORT"}, ports.ErrorKindTransport, true},
		{"timeout", llmFailure{Code: "TIMEOUT"}, ports.ErrorKindTimeout, true},
		{"invalid", llmFailure{Code: "INVALID_REQUEST"}, ports.ErrorKindProvider, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var runErr *ports.RunError
			if err := classifyFailure(test.failure); !errors.As(err, &runErr) || runErr.Kind != test.kind || runErr.Retryable != test.retryable {
				t.Fatalf("unexpected classification: %v", err)
			}
		})
	}

	provider := New(validConfig(t, "auth-error"), fixedID("run-1"))
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(*agentv1.AgentEvent) error { return nil })
	if reason, retryable := ports.FailureDetails(err); retryable || reason != "DeepSeek authentication failed" {
		t.Fatalf("unexpected public failure: %q retryable=%v", reason, retryable)
	}
	if got := provider.Describe(); got.GetHealth() != commonv1.HealthState_HEALTH_STATE_UNAVAILABLE || strings.Contains(got.GetUnavailableReason(), "not-a-real-key") {
		t.Fatalf("unexpected post-auth health: %#v", got)
	}
}

func validConfig(t *testing.T, mode string) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cordis := t.TempDir() + "/cordis.yml"
	if err := os.WriteFile(cordis, []byte("plugins: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		Enabled: true, Environment: "test", APIKey: "not-a-real-key",
		BaseURL: "http://127.0.0.1:18080", Model: DefaultModel, Timeout: 4 * time.Second,
		RuntimePath: executable, CordisConfigPath: cordis,
		runtimeArgs: []string{"-test.run=^TestDeepSeekRuntimeHelper$"},
		runtimeEnv:  []string{"WORKOS_DEEPSEEK_FIXTURE_MODE=" + mode},
	}
}

func TestDeepSeekRuntimeHelper(t *testing.T) {
	mode := os.Getenv("WORKOS_DEEPSEEK_FIXTURE_MODE")
	if mode == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	readRequest := func(method string) map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(11)
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil || request["method"] != method {
			os.Exit(12)
		}
		return request
	}
	respond := func(id float64, result any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	notify := func(method string, params any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	seq := int64(0)
	event := func(sessionID, eventType string, data any) {
		notify("session.event", map[string]any{"sessionId": sessionID, "event": map[string]any{
			"seq": seq, "time": 0, "type": eventType, "data": data,
		}})
		seq++
	}

	initialize := readRequest("initialize")
	params, _ := initialize["params"].(map[string]any)
	if params["provider"] != "deepseek-official" || params["model"] != DefaultModel || os.Getenv("DEEPSEEK_API_KEY") != "not-a-real-key" || os.Getenv("WORKOS_DATABASE_URL") != "" {
		os.Exit(13)
	}
	respond(initialize["id"].(float64), map[string]any{"serverInfo": map[string]string{"name": "deepseek-harness-sdk-runtime", "version": "0.0.1"}})
	prompt := readRequest("session/prompt")
	promptParams, _ := prompt["params"].(map[string]any)
	sessionID, _ := promptParams["sessionId"].(string)
	messageID := "fixture-message"
	event(sessionID, "agent/inbox/spliced", map[string]any{"target": "next-turn", "start": 0, "inserted": []any{map[string]any{"id": messageID}}})
	notify("session.status", map[string]any{"sessionId": sessionID, "status": "running"})
	event(sessionID, "agent/inbox/spliced", map[string]any{"target": "next-turn", "start": 0, "removedCount": 1, "inserted": []any{}})

	switch mode {
	case "hang":
		select {}
	case "malformed":
		fmt.Printf("{not-json:%s}\n", os.Getenv("DEEPSEEK_API_KEY"))
		os.Exit(0)
	case "early-eof":
		os.Exit(0)
	case "oversized":
		fmt.Println(strings.Repeat("x", maximumJSONRPCLineBytes+1))
		os.Exit(0)
	case "unknown-event":
		event(sessionID, "vendor/mystery", map[string]any{})
		os.Exit(0)
	case "unexpected-content":
		event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "tool-call-delta"}})
		os.Exit(0)
	case "auth-error":
		event(sessionID, "turn/end", map[string]any{"turn": 0, "reason": map[string]any{"kind": "error", "error": map[string]any{
			"message": "credential not-a-real-key rejected", "code": "AUTH", "status": 401,
		}}})
		notify("session.status", map[string]any{"sessionId": sessionID, "status": "idle"})
		respond(prompt["id"].(float64), map[string]any{"messageId": messageID})
		select {}
	case "success":
		event(sessionID, "turn/start", map[string]any{"turn": 0})
		event(sessionID, "step/start", map[string]any{"turn": 0, "step": 0})
		event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "block-start", "index": 0, "blockType": "text"}})
		for _, text := range []string{"Hel", "", "lo"} {
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "text-delta", "index": 0, "text": text}})
		}
		event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "block-end", "index": 0, "block": map[string]any{"type": "text", "text": "Hello"}}})
		usage := map[string]any{"inputTokens": 7, "cacheReadTokens": 2, "cacheWriteTokens": 1, "outputTokens": 5}
		event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "usage", "usage": usage}})
		event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "finish", "reason": map[string]any{"kind": "completed"}}})
		event(sessionID, "assistant/message", map[string]any{"turn": 0, "step": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Hello"}}}, "usage": usage})
		event(sessionID, "step/end", map[string]any{"turn": 0, "step": 0, "reason": map[string]any{"kind": "completed"}})
		event(sessionID, "turn/end", map[string]any{"turn": 0, "reason": map[string]any{"kind": "completed"}})
		notify("session.status", map[string]any{"sessionId": sessionID, "status": "idle"})
		respond(prompt["id"].(float64), map[string]any{"messageId": messageID})
	default:
		os.Exit(14)
	}

	shutdown := readRequest("shutdown")
	respond(shutdown["id"].(float64), map[string]any{})
	os.Exit(0)
}
