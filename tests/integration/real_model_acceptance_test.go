//go:build integration && realmodelgate

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
)

func realModelEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_REAL_MODEL_GATE_" + name)
	if value == "" {
		t.Fatalf("run tools/real-model-acceptance/gate.sh: missing %s", name)
	}
	return value
}

// Live service only; the entry point provisions a fresh bound workspace and
// Vault, with an upstream budget guard. No ordinary test spends model quota.
func TestRealModelDeepSeekContinuousSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
	defer client.CloseIdleConnections()
	origin := realModelEnv(t, "URL")
	project := realModelEnv(t, "PROJECT")
	workspace := realModelEnv(t, "WORKSPACE")
	sessions := agentv1connect.NewAgentSessionServiceClient(client, origin)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, origin)
	created, err := sessions.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ProjectId: project, IdempotencyKey: "real-development"}))
	if err != nil {
		t.Fatal(err)
	}
	session := created.Msg.Session.Id
	fmt.Printf("LIVE_PROJECT=%s LIVE_SESSION=%s\n", project, session)
	turns := []string{
		"In /workspace create a tiny JavaScript CommonJS module calculate.cjs exporting a function that doubles a number. Create calculate.test.cjs using node:test and node:assert/strict to test it (include input 3). Execute node --test calculate.test.cjs with the Bash tool, fix any failure, then reply TURN1_OK. Do not ask questions, install packages, or use network. Use only these two files. Keep responses concise.",
		"Continue the previous work in this SAME native session. Change the function you just wrote to triple its input, update its tests, and execute those tests using Bash. Fix any failures and reply TURN2_OK. Do not create any other files or use network. Keep responses concise.",
	}
	for index, prompt := range turns {
		key := fmt.Sprintf("real-turn-%d", index+1)
		_, err = sessions.SubmitSessionInput(ctx, connect.NewRequest(&agentv1.SubmitSessionInputRequest{SessionId: session, ClientInputId: key, Text: prompt}))
		if err != nil {
			t.Fatal(err)
		}
		var task *agentv1.AgentTask
		for deadline := time.Now().Add(7 * time.Minute); time.Now().Before(deadline); {
			input, e := sessions.GetSessionInput(ctx, connect.NewRequest(&agentv1.GetSessionInputRequest{SessionId: session, ClientInputId: key}))
			if e != nil {
				t.Fatal(e)
			}
			if input.Msg.Input.TaskId != "" {
				result, e := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: input.Msg.Input.TaskId}))
				if e != nil {
					t.Fatal(e)
				}
				task = result.Msg.Task
				if task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED || task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
					break
				}
			}
			time.Sleep(time.Second)
		}
		if task == nil || task.State != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
			t.Fatalf("turn %d did not complete", index+1)
		}
		stream, e := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: task.Id}))
		if e != nil {
			t.Fatal(e)
		}
		var in, out int64
		tool, answer := false, false
		for stream.Receive() {
			event := stream.Msg().Event
			if u := event.GetUsageRecorded(); u != nil {
				in += u.InputTokens
				out += u.OutputTokens
			}
			if event.GetToolCallStarted() != nil {
				tool = true
			}
			if a := event.GetAssistantMessage(); a != nil && strings.Contains(a.Text, fmt.Sprintf("TURN%d_OK", index+1)) {
				answer = true
			}
		}
		if e := stream.Err(); e != nil {
			t.Fatal(e)
		}
		if !tool || !answer || in <= 0 || out <= 0 {
			t.Fatalf("missing tool/answer/usage: %t %t %d %d", tool, answer, in, out)
		}
		for _, name := range []string{"calculate.cjs", "calculate.test.cjs"} {
			if _, e := os.Stat(filepath.Join(workspace, name)); e != nil {
				t.Fatal(e)
			}
		}
		// Independently execute the model-written suite in the same isolated image,
		// and check the observable module behavior without trusting model prose.
		expected := 6
		if index == 1 {
			expected = 9
		}
		command := fmt.Sprintf("node --test calculate.test.cjs && node -e \"if(require('./calculate.cjs')(3)!==%d)process.exit(1)\"", expected)
		check := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "-v", workspace+":/workspace:ro", "-w", "/workspace", "workos-workspace-runtime:dev", "/bin/sh", "-c", command)
		if e := check.Run(); e != nil {
			t.Fatalf("independent project tests/behavior failed: %v", e)
		}
		fmt.Printf("LIVE_TURN=%d TASK=%s INPUT_TOKENS=%d OUTPUT_TOKENS=%d CODE_AND_TESTS=PASS\n", index+1, task.Id, in, out)
	}
}
