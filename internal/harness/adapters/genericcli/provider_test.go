package genericcli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestProviderAcceptsCanonicalNDJSON(t *testing.T) {
	provider := helperProvider(t, "valid", time.Second*time.Duration(helperTimeoutScale))
	var events []*agentv1.AgentEvent
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "hello"}, Emit: func(event *agentv1.AgentEvent) error {
		events = append(events, event)
		return nil
	}, Artifacts: nil})
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
			provider := helperProvider(t, test.mode, time.Second*time.Duration(helperTimeoutScale))
			err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: "hello"}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: nil})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestProviderEnforcesTimeout(t *testing.T) {
	provider := helperProvider(t, "timeout", 25*time.Millisecond)
	err := provider.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(*agentv1.AgentEvent) error { return nil }, Artifacts: nil})
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
	provider, err := New(Config{
		Executable: executable,
		Args:       []string{"-test.run=^TestGenericCLIHelperProcess$", "--", "helper-mode=" + mode},
		Timeout:    timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestGenericCLIHelperProcess(t *testing.T) {
	mode := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "helper-mode=") {
			mode = strings.TrimPrefix(arg, "helper-mode=")
		}
	}
	if mode == "" {
		return
	}
	if mode == "descendant" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	line, err := bufio.NewReader(os.Stdin).ReadBytes('\n')
	envelope := &harnessv1.HarnessCLIRequest{}
	if err != nil || protojson.Unmarshal(line, envelope) != nil || envelope.GetProtocolVersion() != protocolVersion || envelope.GetTaskId() != "task-1" {
		os.Exit(3)
	}
	event := func(raw string) { fmt.Printf("{\"event\":%s}\n", raw) }
	started := `{"runStarted":{"runId":"run-1","providerId":"generic-cli"}}`
	completed := `{"runCompleted":{"summary":"done"}}`
	if strings.HasPrefix(mode, "structured-") {
		artifact := func(kind string) {
			output := &executionv1.TaskArtifactOutput{OutputKey: kind, Title: "Structured fixture"}
			if kind == "markdown" {
				output.Content = &executionv1.TaskArtifactOutput_Markdown{Markdown: &executionv1.MarkdownArtifactContent{Content: []byte("# Fixture review\n")}}
			} else {
				output.Content = &executionv1.TaskArtifactOutput_UnifiedDiff{UnifiedDiff: &executionv1.UnifiedDiffArtifactContent{Content: []byte("--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n")}}
			}
			body, _ := protojson.Marshal(&harnessv1.HarnessCLIResponse{Payload: &harnessv1.HarnessCLIResponse_Artifact{Artifact: output}})
			fmt.Println(string(body))
		}
		if mode == "structured-context" && (len(envelope.GetContext()) != 1 || string(envelope.Context[0].GetContent()) != "synthetic pinned context" || envelope.Context[0].GetArtifactId() != "artifact-1" || envelope.Context[0].GetDigest() != "sha256:fixture") {
			os.Exit(10)
		}
		if mode != "structured-before-start" {
			event(started)
		}
		artifact("markdown")
		if mode == "structured-duplicate" {
			artifact("markdown")
		}
		if mode != "structured-missing" {
			artifact("diff")
		}
		if mode == "structured-failed" {
			event(`{"runFailed":{"reason":"fixture failed"}}`)
		} else {
			event(completed)
		}
		if mode == "structured-after-terminal" {
			artifact("markdown")
		}
		if mode == "structured-exit-failure" {
			os.Exit(9)
		}
		os.Exit(0)
	}
	switch mode {
	case "descendant-pipe":
		executable, _ := os.Executable()
		child := exec.Command(executable, "-test.run=^TestGenericCLIHelperProcess$", "--", "helper-mode=descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(7)
		}
		event(fmt.Sprintf(`{"runStarted":{"runId":"%d","providerId":"generic-cli"}}`, child.Process.Pid))
		time.Sleep(time.Minute)
	case "false-completion":
		event(started)
		event(completed)
		os.Exit(8)
	case "check-env":
		if os.Getenv("WORKOS_CLI_SYNTHETIC_SECRET") != "" {
			os.Exit(5)
		}
		event(started)
		event(completed)
	case "stderr-secret":
		fmt.Fprintln(os.Stderr, "synthetic-secret-not-for-logs")
		os.Exit(6)
	case "byte-flood":
		event(started)
		for range 100 {
			event(fmt.Sprintf(`{"assistantMessage":{"text":"%s"}}`, strings.Repeat("x", 64*1024)))
		}
		event(completed)
	case "long-line":
		event(started)
		event(fmt.Sprintf(`{"assistantMessage":{"text":"%s"}}`, strings.Repeat("x", maxEventBytes)))
		event(completed)
	case "flood":
		event(started)
		for range 2048 {
			event(`{"assistantMessage":{"text":"bounded fixture"}}`)
		}
		event(completed)
	case "forged-artifact":
		event(started)
		event(`{"artifactCreated":{"artifactId":"unminted","artifactType":"document.markdown.v1"}}`)
		event(completed)
	case "valid":
		event(started)
		event(`{"assistantMessage":{"text":"hello"}}`)
		event(completed)
	case "malformed":
		event("not-json")
	case "missing-terminal":
		event(started)
	case "spoofed-metadata":
		event(`{"id":"not-owned-here","runStarted":{"runId":"run-1","providerId":"generic-cli"}}`)
	case "after-terminal":
		event(started)
		event(completed)
		event(`{"assistantMessage":{"text":"late"}}`)
	case "legacy":
		fmt.Println(started)
	case "timeout-long":
		event(started)
		time.Sleep(10 * time.Second)
	case "timeout":
		time.Sleep(time.Second)
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func TestProviderDoesNotInheritParentEnvironment(t *testing.T) {
	t.Setenv("WORKOS_CLI_SYNTHETIC_SECRET", "synthetic-secret-not-for-child")
	p := helperProvider(t, "check-env", time.Second*time.Duration(helperTimeoutScale))
	if err := p.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(*agentv1.AgentEvent) error { return nil }}); err != nil {
		t.Fatalf("parent environment reached child: %v", err)
	}
}
func TestProviderDiscardsStderrAndRejectsUnadvertisedEvents(t *testing.T) {
	for _, mode := range []string{"stderr-secret", "flood", "byte-flood", "long-line", "forged-artifact"} {
		t.Run(mode, func(t *testing.T) {
			p := helperProvider(t, mode, time.Second*time.Duration(helperTimeoutScale))
			err := p.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(*agentv1.AgentEvent) error { return nil }})
			if err == nil {
				t.Fatal("unsafe output accepted")
			}
			if strings.Contains(err.Error(), "synthetic-secret-not-for-logs") {
				t.Fatal("stderr reached the error")
			}
		})
	}
}

func TestProviderDoesNotPublishCompletionBeforeSuccessfulExit(t *testing.T) {
	p := helperProvider(t, "false-completion", time.Second*time.Duration(helperTimeoutScale))
	terminal := false
	err := p.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(event *agentv1.AgentEvent) error { terminal = terminal || isTerminal(event); return nil }})
	if err == nil || terminal {
		t.Fatalf("failed child published completion: terminal=%v err=%v", terminal, err)
	}
}

func TestProviderCancellationKillsDescendantAndClosesInheritedPipe(t *testing.T) {
	p := helperProvider(t, "descendant-pipe", 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{}, Emit: func(event *agentv1.AgentEvent) error {
			if event.GetRunStarted() != nil {
				pid, err := strconv.Atoi(event.GetRunStarted().GetRunId())
				if err != nil {
					return err
				}
				child <- pid
			}
			return nil
		}})
	}()
	var pid int
	select {
	case pid = <-child:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not start descendant")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled execution succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descendant-held pipe blocked cancellation")
	}
	// A reparented zombie is no longer executing; its namespace init owns reaping.
	deadline := time.Now().Add(time.Second)
	for {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(state))
		if len(fields) >= 3 && fields[2] == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant remains alive after cancellation: pid=%d", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProviderRejectsOversizeRequestBeforeStartingChild(t *testing.T) {
	p, err := New(Config{Executable: "/unavailable/workos-cli"})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Run(context.Background(), ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{Goal: strings.Repeat("x", maxRequestBytes)}, Emit: func(*agentv1.AgentEvent) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("oversize request reached executable: %v", err)
	}
}

func TestProviderHealthTracksExecutableAvailability(t *testing.T) {
	path := t.TempDir() + "/synthetic-private-path"
	provider, err := New(Config{Executable: path})
	if err != nil {
		t.Fatal(err)
	}
	assertHealth := func(want commonv1.HealthState) {
		t.Helper()
		info := provider.Describe()
		if info.GetHealth() != want || strings.Contains(info.GetUnavailableReason(), path) {
			t.Fatalf("health=%v reason=%q", info.GetHealth(), info.GetUnavailableReason())
		}
	}
	assertHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE)
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	assertHealth(commonv1.HealthState_HEALTH_STATE_HEALTHY)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE)
}
