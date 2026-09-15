package deepseek

import (
	"context"
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

	// The second turn continues the same native context: the fixture's
	// COUNT_HISTORY answer reports the turn-one markers still present in
	// the request history (TOOLOUT is turn one's assistant answer).
	events = nil
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
