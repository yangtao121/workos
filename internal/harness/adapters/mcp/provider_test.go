package mcp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

// buildFixture compiles the versioned stdio fixture once per test run: the
// same binary the full-stack gates execute.
func buildFixture(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mcp-server-fixture")
	command := exec.Command("go", "build", "-o", binary, "github.com/yangtao121/workos/cmd/mcp-server-fixture")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build mcp fixture: %v: %s", err, output)
	}
	return binary
}

func fixtureProvider(t *testing.T, mode string, timeout time.Duration) *Provider {
	t.Helper()
	provider := New(Config{
		Enabled: true, Server: buildFixture(t), Timeout: timeout,
		fixtureMode: mode, fixtureEnv: []string{"MCP_FIXTURE_MODE=" + mode},
	})
	info := provider.Describe()
	if info.GetHealth() != commonv1.HealthState_HEALTH_STATE_HEALTHY {
		t.Fatalf("fixture provider unhealthy: %s", info.GetUnavailableReason())
	}
	return provider
}

func runMCP(t *testing.T, provider *Provider, mutate func(*ports.Execution)) ([]*agentv1.AgentEvent, error) {
	t.Helper()
	execution := ports.Execution{
		TaskID: "task-1",
		Input:  &agentv1.AgentTaskInput{Goal: "review the fixture"},
		Emit:   func(*agentv1.AgentEvent) error { return nil },
	}
	if mutate != nil {
		mutate(&execution)
	}
	var events []*agentv1.AgentEvent
	execution.Emit = func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	}
	err := provider.Run(context.Background(), execution)
	return events, err
}

func TestMCPDeclaresDegradedCapabilities(t *testing.T) {
	provider := fixtureProvider(t, "ok", time.Minute)
	capabilities := provider.Describe().GetCapabilities()
	if capabilities.GetStreaming() || capabilities.GetUsageReporting() || capabilities.GetHardTokenBudget() ||
		capabilities.GetHardRuntimeDeadline() || capabilities.GetStructuredArtifacts() ||
		capabilities.GetRequiresTaskCredentialLease() || capabilities.GetPersistentSessions() {
		t.Fatalf("MCP must declare the honest degraded subset: %#v", capabilities)
	}
	if len(capabilities.GetSupportedArtifactTypes()) != 0 || len(capabilities.GetSupportedContextRefTypes()) != 0 {
		t.Fatal("MCP declares no artifact or context support")
	}
}

func TestMCPRunsDeterministicTask(t *testing.T) {
	provider := fixtureProvider(t, "ok", 30*time.Second)
	events, err := runMCP(t, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].GetRunStarted().GetProviderId() != ProviderID ||
		events[1].GetAssistantMessage().GetText() != "mcp fixture result for: review the fixture" ||
		events[2].GetRunCompleted() == nil {
		t.Fatalf("unexpected canonical stream: %#v", events)
	}
}

func TestMCPRefusesOutOfContractInputs(t *testing.T) {
	provider := fixtureProvider(t, "ok", 30*time.Second)
	for name, mutate := range map[string]func(*ports.Execution){
		"credential lease": func(e *ports.Execution) {
			e.Credential = &ports.CredentialLease{ID: "l", ConsumerID: "mcp", Purpose: "provider-api-key.v1", Secret: []byte("x"), ExpiresAt: time.Now().Add(time.Minute)}
		},
		"context":    func(e *ports.Execution) { e.Context = []ports.ContextDocument{{RefType: "artifact.review.v1"}} },
		"artifacts":  func(e *ports.Execution) { e.Input.OutputArtifactTypes = []string{"document.markdown.v1"} },
		"budget":     func(e *ports.Execution) { e.Input.Budget = &agentv1.AgentBudget{MaxTokens: 100} },
		"empty goal": func(e *ports.Execution) { e.Input.Goal = "" },
		"role":       func(e *ports.Execution) { e.Input.Role = "coding" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runMCP(t, provider, mutate); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestMCPFailureMatrixConverges(t *testing.T) {
	for _, test := range []struct {
		name, mode, want string
	}{
		{"protocol drift", "protocol-error", "mcp server rejected the call"},
		{"missing tool", "no-tool", "does not expose the task tool"},
		{"tool error", "tool-error", "mcp server rejected the call"},
		{"oversize line", "oversize", "bounded output budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := fixtureProvider(t, test.mode, 15*time.Second)
			events, err := runMCP(t, provider, nil)
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
			for _, event := range events {
				if event.GetRunCompleted() != nil {
					t.Fatal("a failed run must not complete")
				}
			}
		})
	}
}

func TestMCPHangAndCrashConverge(t *testing.T) {
	hang := fixtureProvider(t, "hang", 1100*time.Millisecond)
	_, err := runMCP(t, hang, nil)
	var runErr *ports.RunError
	if err == nil || !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindTimeout {
		t.Fatalf("expected timeout verdict, got %v", err)
	}

	crash := fixtureProvider(t, "crash", 30*time.Second)
	events, err := runMCP(t, crash, nil)
	if err == nil {
		t.Fatal("child crash accepted as success")
	}
	for _, event := range events {
		if event.GetRunCompleted() != nil {
			t.Fatal("crashed run must not complete")
		}
	}
}

func TestMCPRejectsRelativeServer(t *testing.T) {
	provider := New(Config{Enabled: true, Server: "mcp-fixture"})
	if provider.Describe().GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY {
		t.Fatal("relative server path accepted")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
