package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

const testSessionID = "0198d7ea-2110-7c42-b659-c5e4d73bd4bb"

const testWorkosToolsPlugin = `export const name = 'workos-tools';
export const inject = ['tools'];
export function apply() {}
`

// sessionTestConfig mirrors validConfig but drives the persistent session
// fake runtime. The fake records every initialize handshake to a counter
// file so process reuse is observable. strict adds the exact-match WorkOS
// tool env expectations the fake runtime asserts at initialize; the
// owner-change respawn test needs them off because it deliberately spawns
// under a different owner.
func sessionTestConfig(t *testing.T, mode, counter string, strict bool) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cordis := t.TempDir() + "/cordis.yml"
	if err := os.WriteFile(cordis, []byte("plugins: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginPath := t.TempDir() + "/" + workosToolsFileName
	if err := os.WriteFile(pluginPath, []byte(testWorkosToolsPlugin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(pluginPath), "workos-session.mjs"), []byte(testWorkosToolsPlugin), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(pluginPath), "workos-workspace.mjs"), []byte(testWorkosToolsPlugin), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeEnv := []string{
		"WORKOS_DEEPSEEK_SESSION_FIXTURE_MODE=" + mode,
		"WORKOS_DEEPSEEK_SESSION_COUNTER=" + counter,
	}
	if strict {
		runtimeEnv = append(runtimeEnv,
			"WORKOS_DEEPSEEK_SESSION_EXPECT_OWNER="+sessionOwner,
			"WORKOS_DEEPSEEK_SESSION_EXPECT_PROJECT="+sessionProject,
			"WORKOS_DEEPSEEK_SESSION_EXPECT_CORE=http://127.0.0.1:18081",
		)
	}
	return Config{
		Enabled: true, Environment: "test",
		BaseURL: "http://127.0.0.1:18080", Model: DefaultModel, Timeout: 4 * time.Second,
		RuntimePath: executable, CordisConfigPath: cordis, WorkosToolsPath: pluginPath,
		CoreURL: "http://127.0.0.1:18081", DeviceID: "0198d7ea-2110-7c42-b659-c5e4d73bd501",
		runtimeArgs: []string{"-test.run=^TestDeepSeekSessionRuntimeHelper$"},
		runtimeEnv:  runtimeEnv,
	}
}

// strictSessionTestConfig is the default shape: every spawn under the fake
// runtime must carry the exact WorkOS tool env facts.
func strictSessionTestConfig(t *testing.T, mode, counter string) Config {
	t.Helper()
	return sessionTestConfig(t, mode, counter, true)
}

// sessionOwner and sessionProject are the server-derived WorkOS tool facts of
// the fake session; the fake runtime asserts they arrive in the child env.
const (
	sessionOwner   = "0198d7ea-2110-7c42-b659-c5e4d73bd502"
	sessionProject = "0198d7ea-2110-7c42-b659-c5e4d73bd503"
)

func initializeCount(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), "initialize")
}

func collectEvents() (*[]*agentv1.AgentEvent, ports.Emit) {
	events := make([]*agentv1.AgentEvent, 0, 12)
	return &events, func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	}
}

func TestSessionManagerReusesOneProcessAcrossTurns(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(strictSessionTestConfig(t, "session-tools", counter), nil)
	defer manager.Shutdown()
	secret := []byte("not-a-real-key")

	proc, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, secret, sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, secret, sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	if proc != reused {
		t.Fatal("a live matching session process was not reused")
	}
	for turn := 0; turn < 2; turn++ {
		events, emit := collectEvents()
		if err := manager.Prompt(context.Background(), proc, "run-1", "turn "+string(rune('0'+turn)), DefaultMaxTokens, 4*time.Second, emit); err != nil {
			t.Fatal(err)
		}
		if got := (*events)[0].GetRunStarted(); got.GetRunId() != "run-1" || got.GetProviderId() != ProviderID {
			t.Fatalf("unexpected first event: %#v", (*events)[0])
		}
		if (*events)[len(*events)-1].GetRunCompleted() == nil {
			t.Fatalf("turn %d did not end with RunCompleted: %#v", turn, *events)
		}
	}
	if count := initializeCount(t, counter); count != 1 {
		t.Fatalf("expected exactly one initialize across two turns, got %d", count)
	}
}

func TestSessionManagerRespawnsOnFingerprintChange(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(strictSessionTestConfig(t, "session-tools", counter), nil)
	defer manager.Shutdown()

	first, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("rotated-key"), sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("a credential rotation reused the previous session process")
	}
	if count := initializeCount(t, counter); count != 2 {
		t.Fatalf("expected a fresh initialize after rotation, got %d", count)
	}
	// The rotated process still serves turns.
	events, emit := collectEvents()
	if err := manager.Prompt(context.Background(), second, "run-2", "hello", DefaultMaxTokens, 4*time.Second, emit); err != nil {
		t.Fatal(err)
	}
	if len(*events) == 0 || (*events)[len(*events)-1].GetRunCompleted() == nil {
		t.Fatalf("rotated session did not complete a turn: %#v", *events)
	}
}

func TestSessionCordisConfigGeneration(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(strictSessionTestConfig(t, "session-tools", counter), nil)
	defer manager.Shutdown()

	if _, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(stateRoot, testSessionID)
	raw, err := os.ReadFile(filepath.Join(stateDir, "cordis.yml"))
	if err != nil {
		t.Fatal(err)
	}
	generated := string(raw)
	for _, id := range cordisRowIDs {
		if !strings.Contains(generated, "- id: "+id+"\n") {
			t.Fatalf("generated cordis config is missing official row %q:\n%s", id, generated)
		}
	}
	// The three sanctioned substitutions: persistence root, sandbox workspace
	// root (the scratch ws dir when no operator workspace is bound), and the
	// llm-deepseek endpoint/model facts.
	if !strings.Contains(generated, "root: "+filepath.Join(stateDir, "persistence")+"\n") {
		t.Fatalf("persistence root substitution missing:\n%s", generated)
	}
	if !strings.Contains(generated, "workspaceRoot: /workspace\n") {
		t.Fatalf("workspace root substitution missing:\n%s", generated)
	}
	if !strings.Contains(generated, "baseURL: http://127.0.0.1:18080\n") || !strings.Contains(generated, "- id: "+DefaultModel+"\n") {
		t.Fatalf("llm-deepseek endpoint facts missing:\n%s", generated)
	}
	// Rows the runtime cannot load must never appear.
	for _, forbidden := range []string{"permission-presets", "bash-sandbox", "compaction", "spill", "pruner", "token-meter", "session-checkpoint"} {
		if strings.Contains(generated, forbidden) {
			t.Fatalf("generated cordis config contains the unloadable row %q:\n%s", forbidden, generated)
		}
	}
	for _, dir := range []string{sessionHomeDir(stateDir), sessionStateDir(stateDir), sessionWorkspaceDir(stateDir), sessionPersistenceDir(stateDir)} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("session subdirectory %s is missing: %v", dir, err)
		}
	}
	if info, err := os.Stat(filepath.Join(stateDir, "runtime.stderr")); err != nil || info.Mode().IsRegular() != true {
		t.Fatalf("runtime stderr capture is missing: %v", err)
	}
	// The operator's single-shot cordis config is ignored for sessions.
	if strings.Contains(generated, "plugins:") {
		t.Fatalf("generated config must not derive from the single-shot config:\n%s", generated)
	}
	// The read-only WorkOS tools row (B04) is configuration-relative: the
	// plugin file must sit beside the generated cordis.yml and the row name
	// must reference it relatively, never as a bare closure package.
	if !strings.Contains(generated, "name: './"+workosToolsFileName+"'\n") {
		t.Fatalf("workos tools row is not configuration-relative:\n%s", generated)
	}
	plugin, err := os.ReadFile(filepath.Join(stateDir, workosToolsFileName))
	if err != nil || string(plugin) != testWorkosToolsPlugin {
		t.Fatalf("workos tools plugin was not copied into the session state directory: %v", err)
	}
}

// TestSessionManagerRespawnsOnOwnerChange proves the WorkOS tool
// authorization facts are part of the session identity: a different
// owner/project pair never reuses a child spawned under another pair.
func TestSessionManagerRespawnsOnOwnerChange(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(sessionTestConfig(t, "session-tools", counter, false), nil)
	defer manager.Shutdown()

	first, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), "0198d7ea-2110-7c42-b659-c5e4d73bd504", sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("an owner change reused the previous session process")
	}
}

// TestSessionManagerRequiresWorkosToolsPlugin proves a session never starts
// with a silently missing tool plugin: the spawn fails closed.
func TestSessionManagerRequiresWorkosToolsPlugin(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	config := strictSessionTestConfig(t, "session-tools", counter)
	config.WorkosToolsPath = filepath.Join(stateRoot, "missing.mjs")
	manager := NewSessionManager(config, nil)
	defer manager.Shutdown()

	_, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject)
	if err == nil {
		t.Fatal("a missing workos tools plugin must fail the session spawn")
	}
	var runErr *ports.RunError
	if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindConfiguration {
		t.Fatalf("unexpected spawn failure classification: %v", err)
	}
}

func TestSessionManagerMapsToolEvents(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(strictSessionTestConfig(t, "session-tools", counter), nil)
	defer manager.Shutdown()
	proc, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	events, emit := collectEvents()
	if err := manager.Prompt(context.Background(), proc, "run-3", "run pwd", DefaultMaxTokens, 4*time.Second, emit); err != nil {
		t.Fatal(err)
	}

	var started *agentv1.ToolCallStarted
	var completed *agentv1.ToolCallCompleted
	terminals := 0
	for _, event := range *events {
		if event.GetToolCallStarted() != nil {
			started = event.GetToolCallStarted()
		}
		if event.GetToolCallCompleted() != nil {
			completed = event.GetToolCallCompleted()
		}
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminals++
		}
	}
	if started == nil || completed == nil {
		t.Fatalf("tool events were not mapped: %#v", *events)
	}
	if started.GetToolCallId() != "call-1" || started.GetToolName() != "bash" {
		t.Fatalf("unexpected tool call started: %#v", started)
	}
	if got := started.GetInput().GetFields()["command"].GetStringValue(); got != "pwd" {
		t.Fatalf("tool call input was not structured: %#v", started.GetInput())
	}
	if completed.GetToolCallId() != "call-1" || !completed.GetSuccess() {
		t.Fatalf("unexpected tool call completed: %#v", completed)
	}
	if got := completed.GetOutput().GetFields()["text"].GetStringValue(); got != "/workspace/ws" {
		t.Fatalf("tool result output was not structured: %#v", completed.GetOutput())
	}
	if terminals != 1 {
		t.Fatalf("expected exactly one terminal event, got %d", terminals)
	}
	var message *agentv1.AssistantMessage
	var usage *agentv1.UsageRecorded
	for _, event := range *events {
		if event.GetAssistantMessage() != nil {
			message = event.GetAssistantMessage()
		}
		if event.GetUsageRecorded() != nil {
			usage = event.GetUsageRecorded()
		}
	}
	if message.GetText() != "Done" {
		t.Fatalf("unexpected assistant message: %#v", message)
	}
	if usage.GetInputTokens() != 12 || usage.GetOutputTokens() != 7 || usage.GetModel() != DefaultModel {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestSessionTurnErrorIsClassified(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	manager := NewSessionManager(strictSessionTestConfig(t, "session-error", counter), nil)
	defer manager.Shutdown()
	proc, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject)
	if err != nil {
		t.Fatal(err)
	}
	events, emit := collectEvents()
	err = manager.Prompt(context.Background(), proc, "run-4", "hello", DefaultMaxTokens, 4*time.Second, emit)
	var runErr *ports.RunError
	if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindAuthentication || runErr.Retryable {
		t.Fatalf("unexpected turn error classification: %v", err)
	}
	if strings.Contains(err.Error(), "not-a-real-key") {
		t.Fatal("turn error exposed the credential secret")
	}
	for _, event := range *events {
		if event.GetRunCompleted() != nil {
			t.Fatal("a failed turn must not emit RunCompleted")
		}
	}
	// The stream is untrusted after the error: the process is gone and the
	// next Ensure spawns a fresh child.
	if processAlive(proc) {
		t.Fatal("failed turn left the session process alive")
	}
	if count := initializeCount(t, counter); count != 1 {
		t.Fatalf("unexpected initialize count after failure: %d", count)
	}
	if _, err := manager.Ensure(context.Background(), testSessionID, "", stateRoot, []byte("not-a-real-key"), sessionOwner, sessionProject); err != nil {
		t.Fatalf("respawn after failure failed: %v", err)
	}
	if count := initializeCount(t, counter); count != 2 {
		t.Fatalf("expected a respawn after failure, got %d initializes", count)
	}
}

func TestProviderRunsSessionTurnsThroughSessionManager(t *testing.T) {
	stateRoot := t.TempDir()
	counter := filepath.Join(stateRoot, "counter")
	provider := New(strictSessionTestConfig(t, "session-tools", counter), fixedID("0198d7ea-2110-7c42-b659-c5e4d73bc401"))
	execution := ports.Execution{
		TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "hello"}, Credential: testLease(),
		Session: &ports.SessionExecution{
			Tools:     func(context.Context, string, map[string]any) (map[string]any, error) { return map[string]any{}, nil },
			SessionID: testSessionID, StateRoot: stateRoot,
			OwnerUserID: sessionOwner, ProjectID: sessionProject,
		},
	}
	for turn := 0; turn < 2; turn++ {
		events, emit := collectEvents()
		execution.Emit = emit
		// Each run consumes its own lease: the adapter zeroizes the secret
		// when a run ends (ADR-0009), exactly like the worker's fresh
		// per-task vault lease.
		execution.Credential = testLease()
		if err := provider.Run(context.Background(), execution); err != nil {
			var runErr *ports.RunError
			errors.As(err, &runErr)
			t.Fatalf("provider session turn failed: %v (cause: %v)", err, runErr.Cause)
		}
		if got := (*events)[0].GetRunStarted(); got.GetRunId() != "0198d7ea-2110-7c42-b659-c5e4d73bc401" || got.GetProviderId() != ProviderID {
			t.Fatalf("unexpected run start on turn %d: %#v", turn, (*events)[0])
		}
		if (*events)[len(*events)-1].GetRunCompleted() == nil {
			t.Fatalf("provider session turn %d did not complete: %#v", turn, *events)
		}
	}
	if count := initializeCount(t, counter); count != 2 {
		t.Fatalf("provider must release the credential process after each task, initializes=%d", count)
	}
	provider.sessions.Shutdown()
}

func TestSessionBufferedNotificationsReplayBeforeWire(t *testing.T) {
	manager := NewSessionManager(strictSessionTestConfig(t, "session-tools", ""), nil)
	defer manager.Shutdown()
	buffered := rpcEnvelope{JSONRPC: jsonRPCVersion, Method: "session.status", Params: []byte(`{"sessionId":"s","status":"starting"}`)}
	proc := &sessionProcess{sessionID: testSessionID, pending: []rpcEnvelope{buffered}}
	first, err := manager.nextFrame(context.Background(), proc)
	if err != nil {
		t.Fatal(err)
	}
	if first.Method != "session.status" {
		t.Fatalf("buffered notification was not replayed first: %#v", first)
	}
	if len(proc.pending) != 0 {
		t.Fatalf("replay did not drain the pending queue: %#v", proc.pending)
	}
	if err := manager.bufferNotification(proc, buffered); err != nil {
		t.Fatal(err)
	}
	if err := manager.bufferNotification(proc, rpcEnvelope{JSONRPC: jsonRPCVersion, Method: "vendor/mystery"}); err == nil {
		t.Fatal("an unknown notification was buffered instead of rejected")
	}
}

// TestDeepSeekSessionRuntimeHelper is a persistent fake of the official
// runtime binary for continuous-session tests. It is executed as a child of
// the package test binary via WORKOS_DEEPSEEK_SESSION_FIXTURE_MODE.
func TestDeepSeekSessionRuntimeHelper(t *testing.T) {
	mode := os.Getenv("WORKOS_DEEPSEEK_SESSION_FIXTURE_MODE")
	if mode == "" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	readRequest := func() map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(11)
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
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

	initialize := readRequest()
	if initialize["method"] != "initialize" {
		os.Exit(12)
	}
	params, _ := initialize["params"].(map[string]any)
	if params["provider"] != "deepseek-official" || params["model"] != DefaultModel || os.Getenv("DEEPSEEK_API_KEY") == "" || os.Getenv("WORKOS_DATABASE_URL") != "" {
		os.Exit(13)
	}
	if cordis := os.Getenv("DSH_CORDIS_CONFIG"); cordis == "" {
		os.Exit(15)
	}
	if cwd := os.Getenv("DSH_CWD"); cwd == "" {
		os.Exit(16)
	}
	// The read-only WorkOS tool context (B04) is injected per session child
	// from the server-derived task facts; the fake runtime asserts the exact
	// owner/project/core facts reach the environment and nothing else leaks.
	if expected := os.Getenv("WORKOS_DEEPSEEK_SESSION_EXPECT_OWNER"); expected != "" {
		if os.Getenv("WORKOS_TOOL_OWNER_ID") != expected {
			os.Exit(22)
		}
		if os.Getenv("WORKOS_TOOL_PROJECT_ID") != os.Getenv("WORKOS_DEEPSEEK_SESSION_EXPECT_PROJECT") {
			os.Exit(23)
		}
		if os.Getenv("WORKOS_TOOL_CORE_URL") != os.Getenv("WORKOS_DEEPSEEK_SESSION_EXPECT_CORE") {
			os.Exit(24)
		}
		if os.Getenv("WORKOS_TOOL_DEVICE_ID") == "" {
			os.Exit(25)
		}
	}
	if counter := os.Getenv("WORKOS_DEEPSEEK_SESSION_COUNTER"); counter != "" {
		file, err := os.OpenFile(counter, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(21)
		}
		_, _ = file.WriteString("initialize\n")
		_ = file.Close()
	}
	respond(initialize["id"].(float64), map[string]any{"serverInfo": map[string]string{"name": "workos-deepseek-session", "version": "0.0.1"}})

	for {
		request := readRequest()
		switch request["method"] {
		case "shutdown":
			respond(request["id"].(float64), map[string]any{})
			os.Exit(0)
		case "session/prompt":
		default:
			os.Exit(12)
		}
		promptParams, _ := request["params"].(map[string]any)
		sessionID, _ := promptParams["sessionId"].(string)
		messageID := "fixture-session-message"
		notify("session.status", map[string]any{"sessionId": sessionID, "status": "running"})
		switch mode {
		case "session-tools":
			event(sessionID, "turn/start", map[string]any{"turn": 0})
			event(sessionID, "step/start", map[string]any{"turn": 0, "step": 0})
			event(sessionID, "session/title", map[string]any{"title": "fixture"})
			event(sessionID, "tool/call", map[string]any{"callId": "call-1", "name": "bash", "arguments": `{"command":"pwd"}`})
			event(sessionID, "tool/result", map[string]any{"message": map[string]any{
				"source": map[string]any{"callId": "call-1"},
				"content": []any{map[string]any{
					"type": "tool-result",
					"content": []any{map[string]any{
						"type": "text", "text": "/workspace/ws",
					}},
				}},
			}})
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "block-start", "index": 0, "blockType": "text"}})
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "text-delta", "index": 0, "text": "Done"}})
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "block-end", "index": 0, "block": map[string]any{"type": "text", "text": "Done"}}})
			usage := map[string]any{"inputTokens": 12, "cacheReadTokens": 0, "cacheWriteTokens": 0, "outputTokens": 7}
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "usage", "usage": usage}})
			event(sessionID, "assistant/chunk", map[string]any{"turn": 0, "step": 0, "chunk": map[string]any{"type": "finish", "reason": map[string]any{"kind": "completed"}}})
			event(sessionID, "assistant/message", map[string]any{"turn": 0, "step": 0, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Done"}}}, "usage": usage})
			event(sessionID, "step/end", map[string]any{"turn": 0, "step": 0, "reason": map[string]any{"kind": "completed"}})
			event(sessionID, "turn/end", map[string]any{"turn": 0, "reason": map[string]any{"kind": "completed"}})
			notify("session.status", map[string]any{"sessionId": sessionID, "status": "idle"})
			respond(request["id"].(float64), map[string]any{"messageId": messageID})
		case "session-error":
			event(sessionID, "turn/end", map[string]any{"turn": 0, "reason": map[string]any{"kind": "error", "error": map[string]any{
				"message": "credential rejected", "code": "AUTH", "status": 401,
			}}})
			notify("session.status", map[string]any{"sessionId": sessionID, "status": "idle"})
			respond(request["id"].(float64), map[string]any{"messageId": messageID})
		default:
			os.Exit(14)
		}
	}
}
