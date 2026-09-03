package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// buildFixture compiles the versioned stdio fixture once per test run: the
// same binary the full-stack gates execute, never a test-local protocol
// reimplementation.
func buildFixture(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "codex-app-server-fixture")
	command := exec.Command("go", "build", "-o", binary, "github.com/yangtao121/workos/cmd/codex-app-server-fixture")
	command.Env = append(os.Environ(), "CODEX_FIXTURE_MODE=")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build codex fixture: %v: %s", err, output)
	}
	return binary
}

func fixtureProvider(t *testing.T, mode string, timeout time.Duration) *Provider {
	t.Helper()
	provider := New(Config{
		Enabled: true, AppServer: buildFixture(t), Timeout: timeout,
		fixtureMode: mode, fixtureEnv: []string{"CODEX_FIXTURE_MODE=" + mode},
	}, ids.UUIDv7{})
	info := provider.Describe()
	if info.GetHealth() != commonv1.HealthState_HEALTH_STATE_HEALTHY {
		t.Fatalf("fixture provider unhealthy: %s", info.GetUnavailableReason())
	}
	return provider
}

func codexLease() *ports.CredentialLease {
	return &ports.CredentialLease{
		ID: "lease-1", TaskLeaseID: "task-lease-1", ConsumerID: ProviderID,
		Purpose: ports.PurposeCodexAuthV1, ExpiresAt: time.Now().Add(time.Minute), Secret: []byte("fixture-codex-material"),
	}
}

func runCodex(t *testing.T, provider *Provider, mutate func(*ports.Execution)) ([]*agentv1.AgentEvent, error) {
	t.Helper()
	execution := ports.Execution{
		TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "review the fixture"}, Credential: codexLease(),
		Emit: func(*agentv1.AgentEvent) error { return nil },
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

func TestCodexStreamsCanonicalRun(t *testing.T) {
	provider := fixtureProvider(t, "ok", 30*time.Second)
	events, err := runCodex(t, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("unexpected canonical stream length %d", len(events))
	}
	if events[0].GetRunStarted().GetProviderId() != ProviderID ||
		events[1].GetAssistantDelta() == nil || events[2].GetAssistantDelta() == nil ||
		events[3].GetAssistantMessage() == nil || events[4].GetUsageRecorded() == nil ||
		events[5].GetRunCompleted() == nil {
		t.Fatalf("canonical stream out of order: %#v", events)
	}
	if events[4].GetUsageRecorded().GetOutputTokens() <= 0 {
		t.Fatal("usage missing")
	}
}

func TestCodexDeclaresHonestCapabilities(t *testing.T) {
	provider := fixtureProvider(t, "ok", time.Minute)
	capabilities := provider.Describe().GetCapabilities()
	if !capabilities.GetStreaming() || !capabilities.GetUsageReporting() ||
		!capabilities.GetHardTokenBudget() || !capabilities.GetHardRuntimeDeadline() ||
		!capabilities.GetRequiresTaskCredentialLease() {
		t.Fatalf("capability declarations drifted: %#v", capabilities)
	}
	if capabilities.GetStructuredArtifacts() || len(capabilities.GetSupportedArtifactTypes()) != 0 ||
		len(capabilities.GetSupportedContextRefTypes()) != 0 {
		t.Fatal("codex fixture contract supports neither structured artifacts nor context")
	}
	if capabilities.GetMaxOutputTokens() != MaximumMaxTokens || capabilities.GetMaxRuntimeSeconds() != int64(MaximumTimeout/time.Second) {
		t.Fatalf("enforced maxima drift: %#v", capabilities)
	}
}

func TestCodexEnforcesTokenBudget(t *testing.T) {
	provider := fixtureProvider(t, "over-budget", 30*time.Second)
	events, err := runCodex(t, provider, func(e *ports.Execution) {
		e.Input = &agentv1.AgentTaskInput{Goal: "count", Budget: &agentv1.AgentBudget{MaxTokens: 10}}
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := 0
	for _, event := range events {
		if recorded := event.GetUsageRecorded(); recorded != nil {
			usage = int(recorded.GetOutputTokens())
		}
	}
	if usage != 10 {
		t.Fatalf("fixture exceeded the hard token budget: reported %d", usage)
	}
}

func TestCodexEnforcesRuntimeDeadline(t *testing.T) {
	provider := fixtureProvider(t, "slow", 1100*time.Millisecond)
	_, err := runCodex(t, provider, nil)
	var runErr *ports.RunError
	if err == nil || !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindTimeout {
		t.Fatalf("expected timeout RunError, got %v", err)
	}
}

func TestCodexRejectsProtocolDrift(t *testing.T) {
	provider := fixtureProvider(t, "out-of-order", 30*time.Second)
	events, err := runCodex(t, provider, nil)
	if err == nil {
		t.Fatal("out-of-order stream accepted")
	}
	for _, event := range events {
		if event.GetRunCompleted() != nil {
			t.Fatal("a terminal completed event leaked into the canonical stream")
		}
	}
}

func TestCodexConvergesOnChildCrash(t *testing.T) {
	provider := fixtureProvider(t, "crash", 30*time.Second)
	events, err := runCodex(t, provider, nil)
	if err == nil {
		t.Fatal("child crash accepted as success")
	}
	for _, event := range events {
		if event.GetRunCompleted() != nil || event.GetUsageRecorded() != nil {
			t.Fatal("crash run must not produce usage or completion")
		}
	}
}

func TestCodexRequiresExactLease(t *testing.T) {
	provider := fixtureProvider(t, "ok", 30*time.Second)
	// No lease at all.
	if _, err := runCodex(t, provider, func(e *ports.Execution) { e.Credential = nil }); err == nil {
		t.Fatal("run without a credential lease was accepted")
	}
	// A lease bound to another provider/purpose.
	foreign := codexLease()
	foreign.ConsumerID, foreign.Purpose = "deepseek", ports.PurposeProviderAPIKeyV1
	if _, err := runCodex(t, provider, func(e *ports.Execution) { e.Credential = foreign }); err == nil {
		t.Fatal("run with a foreign lease was accepted")
	}
}

func TestCodexRefusesOutOfContractInputs(t *testing.T) {
	provider := fixtureProvider(t, "ok", 30*time.Second)
	for name, mutate := range map[string]func(*ports.Execution){
		"artifact outputs": func(e *ports.Execution) { e.Input.OutputArtifactTypes = []string{"document.markdown.v1"} },
		"context refs":     func(e *ports.Execution) { e.Input.ContextRefs = []*agentv1.ContextRef{{Type: "artifact.review.v1"}} },
		"capabilities":     func(e *ports.Execution) { e.Input.RequestedCapabilities = []string{"tools"} },
		"empty goal":       func(e *ports.Execution) { e.Input.Goal = "   " },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runCodex(t, provider, mutate); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestCodexRejectsRelativeAppServer(t *testing.T) {
	provider := New(Config{Enabled: true, AppServer: "codex-fixture"}, ids.UUIDv7{})
	if provider.Describe().GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY {
		t.Fatal("relative app server path accepted")
	}
}
