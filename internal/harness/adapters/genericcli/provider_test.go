package genericcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
)

func TestProviderAcceptsCanonicalNDJSON(t *testing.T) {
	provider := helperProvider(t, "valid", time.Second)
	var events []*agentv1.AgentEvent
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].GetRunStarted() == nil || events[2].GetRunCompleted() == nil {
		t.Fatalf("unexpected canonical events: %#v", events)
	}
}

func TestProviderRejectsMalformedAndIncompleteStreams(t *testing.T) {
	for _, test := range []struct {
		mode, want string
	}{
		{"malformed", "decode generic CLI event"},
		{"missing-terminal", "without a terminal event"},
		{"spoofed-metadata", "Core-owned event metadata"},
		{"after-terminal", "after a terminal event"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			provider := helperProvider(t, test.mode, time.Second)
			err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{Goal: "hello"}, func(*agentv1.AgentEvent) error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestProviderEnforcesTimeout(t *testing.T) {
	provider := helperProvider(t, "timeout", 25*time.Millisecond)
	err := provider.Run(context.Background(), "task-1", &agentv1.AgentTaskInput{}, func(*agentv1.AgentEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func TestProviderRequiresAbsoluteExecutable(t *testing.T) {
	if _, err := New(Config{Executable: "agent-cli"}); err == nil {
		t.Fatal("expected relative executable to be rejected")
	}
}

func helperProvider(t *testing.T, mode string, timeout time.Duration) *Provider {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKOS_GENERICCLI_HELPER", mode)
	provider, err := New(Config{
		Executable: executable,
		Args:       []string{"-test.run=^TestGenericCLIHelperProcess$"},
		Timeout:    timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestGenericCLIHelperProcess(t *testing.T) {
	mode := os.Getenv("WORKOS_GENERICCLI_HELPER")
	if mode == "" {
		return
	}
	var envelope struct {
		Version string          `json:"version"`
		TaskID  string          `json:"taskId"`
		Input   json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(bufio.NewReader(os.Stdin)).Decode(&envelope); err != nil || envelope.Version != "workos.harness-cli/v1" || envelope.TaskID != "task-1" {
		os.Exit(3)
	}
	started := `{"runStarted":{"runId":"run-1","providerId":"generic-cli"}}`
	completed := `{"runCompleted":{"summary":"done"}}`
	switch mode {
	case "valid":
		fmt.Println(started)
		fmt.Println(`{"assistantMessage":{"text":"hello"}}`)
		fmt.Println(completed)
	case "malformed":
		fmt.Println("not-json")
	case "missing-terminal":
		fmt.Println(started)
	case "spoofed-metadata":
		fmt.Println(`{"id":"not-owned-here","runStarted":{"runId":"run-1","providerId":"generic-cli"}}`)
	case "after-terminal":
		fmt.Println(started)
		fmt.Println(completed)
		fmt.Println(`{"assistantMessage":{"text":"late"}}`)
	case "timeout":
		time.Sleep(time.Second)
	default:
		os.Exit(4)
	}
	os.Exit(0)
}
