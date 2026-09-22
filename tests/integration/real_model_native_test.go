//go:build integration && realmodelgate

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
)

// Only the explicit spending gate runs this test. The workspace is a clean
// committed fixture; model claims are checked against files and Core facts.
func TestRealModelDeepSeekNativeAutomation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
	defer client.CloseIdleConnections()
	sessions := agentv1connect.NewAgentSessionServiceClient(client, realModelEnv(t, "URL"))
	tasks := agentv1connect.NewAgentTaskServiceClient(client, realModelEnv(t, "URL"))
	project, workspace := realModelEnv(t, "PROJECT"), realModelEnv(t, "WORKSPACE")
	created, err := sessions.CreateSession(ctx, connect.NewRequest(&agentv1.CreateSessionRequest{ProjectId: project, IdempotencyKey: "real-native-automation"}))
	if err != nil {
		t.Fatal(err)
	}
	session := created.Msg.Session.Id
	get := func() *agentv1.AgentSession {
		t.Helper()
		r, e := sessions.GetSession(ctx, connect.NewRequest(&agentv1.GetSessionRequest{SessionId: session}))
		if e != nil {
			t.Fatal(e)
		}
		return r.Msg.Session
	}
	run := func(key, text string, directive *agentv1.SessionDirective) []*agentv1.AgentEvent {
		t.Helper()
		_, e := sessions.SubmitSessionInput(ctx, connect.NewRequest(&agentv1.SubmitSessionInputRequest{SessionId: session, ClientInputId: key, Text: text, Directive: directive}))
		if e != nil {
			t.Fatal(e)
		}
		taskID := ""
		completed := false
		for deadline := time.Now().Add(6 * time.Minute); time.Now().Before(deadline); {
			input, e := sessions.GetSessionInput(ctx, connect.NewRequest(&agentv1.GetSessionInputRequest{SessionId: session, ClientInputId: key}))
			if e != nil {
				t.Fatal(e)
			}
			taskID = input.Msg.Input.TaskId
			if taskID != "" {
				task, e := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
				if e != nil {
					t.Fatal(e)
				}
				if task.Msg.Task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
					completed = true
					break
				}
				if task.Msg.Task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED || task.Msg.Task.State == agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
					t.Fatalf("live %s task %s failed", key, taskID)
				}
			}
			time.Sleep(time.Second)
		}
		if !completed {
			t.Fatalf("live %s timed out", key)
		}
		for i := 0; i < 30 && get().ActiveTaskId != ""; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		stream, e := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID}))
		if e != nil {
			t.Fatal(e)
		}
		events := []*agentv1.AgentEvent{}
		var inputTokens, outputTokens int64
		for stream.Receive() {
			event := stream.Msg().Event
			events = append(events, event)
			if u := event.GetUsageRecorded(); u != nil {
				inputTokens += u.InputTokens
				outputTokens += u.OutputTokens
			}
		}
		if e := stream.Err(); e != nil {
			t.Fatal(e)
		}
		if inputTokens <= 0 || outputTokens <= 0 {
			t.Fatal("missing live aggregate usage")
		}
		fmt.Printf("LIVE_NATIVE=%s TASK=%s INPUT_TOKENS=%d OUTPUT_TOKENS=%d\n", key, taskID, inputTokens, outputTokens)
		return events
	}
	run("children", "Use the subagent tool exactly twice in parallel, with two independent foreground tasks. Ask the first child to use bash to create child-one.txt containing exactly ONE followed by a newline in its own workspace, then stop. Ask the second child to create child-two.txt containing exactly TWO followed by a newline in its own workspace, then stop. Do not change files in the parent workspace, do not merge, and do not ask questions. Summarize the two child results briefly. No network or package installation.", nil)
	children := get().Delegations
	if len(children) != 2 {
		t.Fatalf("expected two native children, got %d", len(children))
	}
	for _, child := range children {
		if child.State != "completed" || child.ResultArtifactId == "" || child.WorktreeId == "" {
			t.Fatalf("live child result is not verified: %s %s", child.Id, child.State)
		}
	}
	if children[0].WorktreeId == children[1].WorktreeId {
		t.Fatal("children shared a worktree")
	}
	verifiedFiles := map[string]bool{}
	for _, child := range children {
		found := 0
		for name, expected := range map[string]string{"child-one.txt": "ONE\n", "child-two.txt": "TWO\n"} {
			content, err := os.ReadFile(filepath.Join(filepath.Dir(workspace), "delegations", child.WorktreeId, "tree", name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || string(content) != expected || verifiedFiles[name] {
				t.Fatalf("isolated child file oracle failed: %s %v", name, err)
			}
			verifiedFiles[name] = true
			found++
		}
		if found != 1 {
			t.Fatal("child did not produce exactly its own file")
		}
	}
	for _, name := range []string{"child-one.txt", "child-two.txt"} {
		if _, err := os.Stat(filepath.Join(workspace, name)); !os.IsNotExist(err) {
			t.Fatal("child modified parent workspace")
		}
	}
	events := run("skill", "Use the skill tool to load project-check. Reply with the unique uppercase marker in the loaded instructions. Do not edit any files or run commands.", nil)
	invoked, marker := false, false
	for _, e := range events {
		if started := e.GetToolCallStarted(); started != nil && started.ToolName == "skill" {
			invoked = true
		}
		if a := e.GetAssistantMessage(); a != nil && strings.Contains(a.Text, "PROJECT_SKILL_PRIVATE_BODY") {
			marker = true
		}
	}
	if !invoked || !marker {
		t.Fatalf("native skill not proved: invoked=%t marker=%t", invoked, marker)
	}
	run("goal", "", &agentv1.SessionDirective{Kind: agentv1.SessionDirectiveKind_SESSION_DIRECTIVE_KIND_CREATE_GOAL, Objective: "In /workspace write native-goal.txt containing exactly GOAL_DONE followed by a newline, then use bash to verify its exact content. Once verified, use get_goal and update_goal to mark this goal complete. Do not create any other files, use network, delegate, or ask questions. Keep all output concise.", MaxRounds: 4})
	goal := get().Goal
	if goal == nil || goal.Phase != "complete" || goal.Armed {
		t.Fatal("native goal did not reach persisted complete state")
	}
	bytes, err := os.ReadFile(filepath.Join(workspace, "native-goal.txt"))
	if err != nil || string(bytes) != "GOAL_DONE\n" {
		t.Fatalf("goal file oracle failed: %v", err)
	}
	fmt.Println("LIVE_NATIVE_CHILDREN_SKILL_GOAL_PASS")
}
