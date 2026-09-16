package deepseek

import (
	"connectrpc.com/connect"
	"context"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/types/known/structpb"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
)

// TestSessionManagerAgainstRealRuntime drives the pinned official runtime
// binary against a local DeepSeek-compatible fixture. It is skipped unless
// WORKOS_DEEPSEEK_RUNTIME_PROBE points at the runtime binary and
// WORKOS_DEEPSEEK_FIXTURE_URL at a live fixture, so the unit suite never
// depends on host tooling.
func TestSessionManagerAgainstRealRuntime(t *testing.T) {
	runtimePath := os.Getenv("WORKOS_DEEPSEEK_RUNTIME_PROBE")
	fixtureURL := os.Getenv("WORKOS_DEEPSEEK_FIXTURE_URL")
	if runtimePath == "" || fixtureURL == "" {
		t.Skip("set WORKOS_DEEPSEEK_RUNTIME_PROBE and WORKOS_DEEPSEEK_FIXTURE_URL to run the real-runtime probe")
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime binary: %v", err)
	}
	if resp, err := http.Get(fixtureURL + "/healthz"); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("fixture not healthy: %v", err)
	}

	stateRoot := os.Getenv("WORKOS_DEEPSEEK_SESSION_PROBE_STATE")
	if stateRoot == "" {
		stateRoot = t.TempDir()
	}
	config := normalizeConfig(Config{
		Enabled:     true,
		Environment: "test",
		BaseURL:     fixtureURL,
		Model:       "deepseek-v4-flash",
		Timeout:     2 * time.Minute,
		RuntimePath: runtimePath,
	})
	// The relative workos-tools row must resolve beside the generated
	// cordis.yml, so the real-runtime probe loads the repository's plugin.
	if plugin := os.Getenv("WORKOS_DEEPSEEK_WORKOS_TOOLS_PLUGIN"); plugin != "" {
		config.WorkosToolsPath = plugin
	} else if _, err := os.Stat(config.WorkosToolsPath); err != nil {
		t.Fatalf("workos tools plugin not readable at %s (set WORKOS_DEEPSEEK_WORKOS_TOOLS_PLUGIN): %v", config.WorkosToolsPath, err)
	}
	workspaceURL := os.Getenv("WORKOS_WORKSPACE_PROBE_URL")
	if workspaceURL == "" {
		t.Fatal("WORKOS_WORKSPACE_PROBE_URL is required: native tools must use Runtime")
	}
	response, err := http.Get(workspaceURL + "/source")
	if err != nil {
		t.Fatal(err)
	}
	source, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	runtimeClient := workloadv1connect.NewWorkspaceExecutionServiceClient(http.DefaultClient, workspaceURL)
	tool := func(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
		arguments, err := structpb.NewStruct(args)
		if err != nil {
			return nil, err
		}
		result, err := runtimeClient.ExecuteWorkspaceOperation(ctx, connect.NewRequest(&workloadv1.ExecuteWorkspaceOperationRequest{OwnerUserId: "0198d7ea-2110-7c42-b659-c5e4d73bc111", ProjectId: "0198d7ea-2110-7c42-b659-c5e4d73bc112", WorkspaceSourceId: string(source), OperationId: (ids.UUIDv7{}).New(), Operation: operation, Arguments: arguments}))
		if err != nil {
			return nil, err
		}
		return result.Msg.GetResult().AsMap(), nil
	}
	manager := NewSessionManager(config, nil)
	defer manager.Shutdown()

	var events []*agentv1.AgentEvent
	emit := func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	}

	turn1Goal := os.Getenv("WORKOS_DEEPSEEK_SESSION_PROBE_GOAL1")
	if turn1Goal == "" {
		turn1Goal = "RUN_PWD please"
	}
	turn2Goal := os.Getenv("WORKOS_DEEPSEEK_SESSION_PROBE_GOAL2")
	if turn2Goal == "" {
		turn2Goal = "COUNT_HISTORY now"
	}
	proc, err := manager.Ensure(context.Background(), "probe-session-1", "", stateRoot, []byte("workos-fixture-only-not-a-real-key"), "", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	proc.tools = tool
	if err := manager.Prompt(context.Background(), proc, "run-1", turn1Goal, 8192, time.Minute, emit); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	var toolStarted, toolCompleted bool
	for _, event := range events {
		switch event.GetEvent().(type) {
		case *agentv1.AgentEvent_ToolCallStarted:
			toolStarted = true
		case *agentv1.AgentEvent_ToolCallCompleted:
			toolCompleted = true
		}
	}
	if !toolStarted || !toolCompleted {
		t.Fatalf("turn 1 missed tool events: started=%v completed=%v", toolStarted, toolCompleted)
	}

	// Reap the credential-bearing process and replace the manager, exactly
	// as a harness-host restart does. The second turn must load native state.
	manager.Shutdown()
	manager = NewSessionManager(config, nil)
	defer manager.Shutdown()
	proc, err = manager.Ensure(context.Background(), "probe-session-1", "", stateRoot, []byte("workos-fixture-only-not-a-real-key"), "", "")
	if err != nil {
		t.Fatalf("resume process: %v", err)
	}

	// The second turn continues the same native context: the fixture's
	// COUNT_HISTORY answer reports the turn-one markers still present in
	// the request history (TOOLOUT is turn one's assistant answer).
	events = nil
	proc.tools = tool
	if err := manager.Prompt(context.Background(), proc, "run-2", turn2Goal, 8192, time.Minute, emit); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	continued := false
	for _, event := range events {
		if message := event.GetAssistantMessage(); message != nil && (strings.Contains(message.GetText(), "markers=TOOLOUT") || strings.Contains(message.GetText(), "has_turn1=true")) {
			continued = true
		}
	}
	if !continued {
		var answers []string
		for _, event := range events {
			if message := event.GetAssistantMessage(); message != nil {
				answers = append(answers, message.GetText())
			}
			if delta := event.GetAssistantDelta(); delta != nil {
				answers = append(answers, delta.GetText())
			}
		}
		t.Fatalf("turn 2 did not continue the native context; answers: %q", answers)
	}

	// The official ask_user_question tool must dispatch through its native
	// capability seam, then receive the answer as an ordinary tool result.
	questionProc, err := manager.Ensure(context.Background(), "probe-question-1", "", stateRoot, []byte("workos-fixture-only-not-a-real-key"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	asked := false
	questionProc.tools = func(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
		if operation != "interaction.ask" {
			return tool(ctx, operation, args)
		}
		questions, ok := args["questions"].([]any)
		if !ok || len(questions) != 1 {
			t.Fatal("native questions not adapted")
		}
		question := questions[0].(map[string]any)
		if question["id"] != "direction" || question["text"] != "Choose the fixture direction" {
			t.Fatal("question content changed")
		}
		asked = true
		return map[string]any{"state": "answered", "answers": []any{map[string]any{"questionId": "direction", "selected": []any{"Continue"}, "text": ""}}}, nil
	}
	events = nil
	if err := manager.Prompt(context.Background(), questionProc, "run-question", "SESSION_QUESTION", 8192, time.Minute, emit); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("official question tool did not reach WorkOS")
	}
	questionCompleted := false
	for _, event := range events {
		if completed := event.GetToolCallCompleted(); completed != nil {
			questionCompleted = true
		}
	}
	if !questionCompleted {
		t.Fatal("native question did not resume")
	}

	// The native session persisted its event log for durability/audit.
	var persisted string
	_ = filepath.Walk(filepath.Join(stateRoot, "probe-session-1", "persistence"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "session.jsonl" {
			persisted = path
		}
		return nil
	})
	if persisted == "" {
		t.Fatal("session persistence log was not written")
	}
	content, err := os.ReadFile(persisted)
	if err != nil || !strings.Contains(string(content), "tool/result") {
		t.Fatalf("session persistence log missing tool facts: %v", err)
	}
}
