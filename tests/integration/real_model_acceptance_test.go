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
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func realModelEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_REAL_MODEL_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/real-model-acceptance/gate.sh (missing %s)", name)
	}
	return value
}

// TestRealModelDeepSeekContinuousSession is the A15 operator gate: the real
// DeepSeek API (never the fixture) drives the pinned official runtime for two
// consecutive native turns on one WorkOS session. Turn one writes an evidence
// file with a real tool call; turn two appends to it and reports the full
// content — only the SAME native context can answer without the path being
// restated. Usage must be reported for both turns. This test costs real API
// quota and never runs from the ordinary suite: tools/real-model-acceptance
// refuses to start without the operator preconditions.
func TestRealModelDeepSeekContinuousSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	gatewayURL := realModelEnv(t, "URL")
	owner := realModelEnv(t, "OWNER")
	device := realModelEnv(t, "DEVICE")
	stateDir := realModelEnv(t, "STATE_DIR")
	if strings.Contains(realModelEnv(t, "BASE_URL"), "127.0.0.1") {
		t.Fatal("the real-model gate must not point at a loopback fixture")
	}

	run := fmt.Sprintf("%d", time.Now().UnixNano())
	sessions := agentv1connect.NewAgentSessionServiceClient(client, gatewayURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, gatewayURL)
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, gatewayURL)

	createProject := connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: "real-model-project-" + run, Name: "Real Model Acceptance " + time.Now().Format("20060102-150405"),
	})
	createProject.Header().Set(identity.UserHeader, owner)
	createProject.Header().Set(identity.DeviceHeader, device)
	project, err := projects.CreateProject(ctx, createProject)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()
	bind := connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
		ProjectId: projectID, ExpectedRevision: project.Msg.GetProject().GetRevision(),
		Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "deepseek"},
	})
	bind.Header().Set(identity.UserHeader, owner)
	bind.Header().Set(identity.DeviceHeader, device)
	for attempt := 0; attempt < 60; attempt++ {
		if _, err := bindings.SetProjectHarnessBinding(ctx, bind); err == nil {
			break
		} else if connect.CodeOf(err) != connect.CodeUnavailable || attempt == 59 {
			t.Fatalf("set binding: %v", err)
		}
		time.Sleep(time.Second)
	}

	create := connect.NewRequest(&agentv1.CreateSessionRequest{
		ProjectId: projectID, IdempotencyKey: "real-model-session-" + run,
	})
	create.Header().Set(identity.UserHeader, owner)
	create.Header().Set(identity.DeviceHeader, device)
	created, err := sessions.CreateSession(ctx, create)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := created.Msg.GetSession().GetId()
	fmt.Printf("WORKOS_REAL_MODEL_SESSION_ID=%s\n", sessionID)
	fmt.Printf("WORKOS_REAL_MODEL_PROJECT_ID=%s\n", projectID)
	defer func() {
		closeRequest := connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sessionID})
		closeRequest.Header().Set(identity.UserHeader, owner)
		closeRequest.Header().Set(identity.DeviceHeader, device)
		_, _ = sessions.CloseSession(context.Background(), closeRequest)
	}()

	waitTerminal := func(t *testing.T, taskID string) (*agentv1.AgentTask, []*agentv1.AgentEvent) {
		t.Helper()
		deadline := time.Now().Add(420 * time.Second)
		for time.Now().Before(deadline) {
			taskRequest := connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID})
			taskRequest.Header().Set(identity.UserHeader, owner)
			taskRequest.Header().Set(identity.DeviceHeader, device)
			response, err := tasks.GetTask(ctx, taskRequest)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			task := response.Msg.GetTask()
			switch task.GetState() {
			case agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED,
				agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED,
				agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED:
				eventsRequest := connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID})
				eventsRequest.Header().Set(identity.UserHeader, owner)
				eventsRequest.Header().Set(identity.DeviceHeader, device)
				stream, err := tasks.WatchTaskEvents(ctx, eventsRequest)
				if err != nil {
					t.Fatalf("watch events: %v", err)
				}
				var events []*agentv1.AgentEvent
				for stream.Receive() {
					events = append(events, stream.Msg().GetEvent())
				}
				return task, events
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("task %s never reached a terminal state", taskID)
		return nil, nil
	}

	usageOf := func(events []*agentv1.AgentEvent) (input, output int64) {
		for _, event := range events {
			if usage := event.GetUsageRecorded(); usage != nil {
				input += usage.GetInputTokens()
				output += usage.GetOutputTokens()
			}
		}
		return input, output
	}

	// Turn one: a real tool write plus a fixed marker answer.
	submit1 := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "real-turn-1", Text: strings.Join([]string{
			"Create a file named real-model-evidence.txt in the current directory containing exactly one line:",
			"workos-real-model-evidence-1",
			"Then reply with exactly: TURN1_OK",
		}, "\n"),
	})
	submit1.Header().Set(identity.UserHeader, owner)
	submit1.Header().Set(identity.DeviceHeader, device)
	turn1, err := sessions.SubmitSessionInput(ctx, submit1)
	if err != nil {
		t.Fatalf("submit turn 1: %v", err)
	}
	fmt.Printf("WORKOS_REAL_MODEL_TASK_1=%s\n", turn1.Msg.GetInput().GetTaskId())
	task1, events1 := waitTerminal(t, turn1.Msg.GetInput().GetTaskId())
	if task1.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("turn 1 terminal state: %s", task1.GetState())
	}
	var sawTool, sawAnswer1 bool
	for _, event := range events1 {
		if event.GetToolCallStarted() != nil {
			sawTool = true
		}
		if message := event.GetAssistantMessage(); message != nil && strings.Contains(message.GetText(), "TURN1_OK") {
			sawAnswer1 = true
		}
	}
	if !sawTool {
		t.Fatal("turn 1 never executed a native tool call")
	}
	if !sawAnswer1 {
		t.Fatal("turn 1 never answered TURN1_OK")
	}
	evidence := filepath.Join(stateDir, sessionID, "ws", "real-model-evidence.txt")
	content1, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("the real model never wrote the evidence file: %v", err)
	}
	if !strings.Contains(string(content1), "workos-real-model-evidence-1") {
		t.Fatalf("evidence file content: %q", string(content1))
	}
	input1, output1 := usageOf(events1)
	if input1 <= 0 || output1 <= 0 {
		t.Fatalf("turn 1 reported no usage: input=%d output=%d", input1, output1)
	}

	// Turn two on the SAME session: only the continued native context knows
	// the file; the answer must echo both lines.
	submit2 := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "real-turn-2", Text: strings.Join([]string{
			"Append a second line to the file you created: workos-real-model-evidence-2",
			"Then reply with the file's complete content and exactly: TURN2_OK",
		}, "\n"),
	})
	submit2.Header().Set(identity.UserHeader, owner)
	submit2.Header().Set(identity.DeviceHeader, device)
	turn2, err := sessions.SubmitSessionInput(ctx, submit2)
	if err != nil {
		t.Fatalf("submit turn 2: %v", err)
	}
	fmt.Printf("WORKOS_REAL_MODEL_TASK_2=%s\n", turn2.Msg.GetInput().GetTaskId())
	task2, events2 := waitTerminal(t, turn2.Msg.GetInput().GetTaskId())
	if task2.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("turn 2 terminal state: %s", task2.GetState())
	}
	sawAnswer2 := false
	for _, event := range events2 {
		if message := event.GetAssistantMessage(); message != nil &&
			strings.Contains(message.GetText(), "workos-real-model-evidence-1") &&
			strings.Contains(message.GetText(), "workos-real-model-evidence-2") &&
			strings.Contains(message.GetText(), "TURN2_OK") {
			sawAnswer2 = true
		}
	}
	if !sawAnswer2 {
		t.Fatal("turn 2 did not answer from the continued native context (both evidence lines + TURN2_OK)")
	}
	content2, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("evidence file unreadable after turn 2: %v", err)
	}
	if !strings.Contains(string(content2), "workos-real-model-evidence-2") {
		t.Fatalf("evidence file after turn 2: %q", string(content2))
	}
	input2, output2 := usageOf(events2)
	if input2 <= 0 || output2 <= 0 {
		t.Fatalf("turn 2 reported no usage: input=%d output=%d", input2, output2)
	}

	fmt.Printf("WORKOS_REAL_MODEL_USAGE turn1=%d/%d turn2=%d/%d (input/output tokens)\n",
		input1, output1, input2, output2)
	fmt.Printf("WORKOS_REAL_MODEL_EVIDENCE_FILE=%s\n", evidence)
}
