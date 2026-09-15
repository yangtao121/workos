//go:build integration && harnesssessiongate

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

func harnessSessionEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_HARNESS_SESSION_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/harness-sessions/gate.sh (missing %s)", name)
	}
	return value
}

// TestHarnessContinuousSessions proves the A04 software chain: through the
// real Gateway/Core/worker/harness-host stack, one continuous session drives
// the pinned official DeepSeek runtime twice — turn one executes a real bash
// tool that writes evidence into the session's private workspace, turn two's
// model request still contains turn one (native context continuation,
// answered by the fixture from history), and replay/cancel/close follow the
// ADR-0030 state machine. The fixture only produces model responses; the
// harness, tools, and file effects are real.
func TestHarnessContinuousSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	gatewayURL := harnessSessionEnv(t, "URL")
	owner := harnessSessionEnv(t, "OWNER")
	device := harnessSessionEnv(t, "DEVICE")
	stateDir := harnessSessionEnv(t, "STATE_DIR")

	// The gate shares the durable dev database; every run uses its own
	// idempotency keys so replays can never cross runs.
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	sessions := agentv1connect.NewAgentSessionServiceClient(client, gatewayURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, gatewayURL)
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, gatewayURL)

	// A real project pinned to deepseek: the task router resolves the
	// provider from the project binding, and the session snapshot pins the
	// same provider.
	createProject := connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: "harness-session-project-" + run, Name: "Harness Session Gate"})
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
	// The provider catalog needs a moment after the harness restarts; the
	// binding retries until deepseek is listed.
	var bound bool
	for attempt := 0; attempt < 30 && !bound; attempt++ {
		if _, err := bindings.SetProjectHarnessBinding(ctx, bind); err == nil {
			bound = true
			break
		} else if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("set binding: %v", err)
		}
		time.Sleep(time.Second)
	}
	if !bound {
		t.Fatal("provider catalog never became available")
	}

	create := connect.NewRequest(&agentv1.CreateSessionRequest{
		ProjectId: projectID, IdempotencyKey: "harness-session-gate-" + run,
	})
	create.Header().Set(identity.UserHeader, owner)
	create.Header().Set(identity.DeviceHeader, device)
	created, err := sessions.CreateSession(ctx, create)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := created.Msg.GetSession().GetId()
	defer func() {
		closeRequest := connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sessionID})
		closeRequest.Header().Set(identity.UserHeader, owner)
		closeRequest.Header().Set(identity.DeviceHeader, device)
		_, _ = sessions.CloseSession(context.Background(), closeRequest)
	}()

	// Idempotent create replay returns the same session.
	replayCreate := connect.NewRequest(&agentv1.CreateSessionRequest{
		ProjectId: projectID, IdempotencyKey: "harness-session-gate-" + run,
	})
	replayCreate.Header().Set(identity.UserHeader, owner)
	replayCreate.Header().Set(identity.DeviceHeader, device)
	replayed, err := sessions.CreateSession(ctx, replayCreate)
	if err != nil || replayed.Msg.GetSession().GetId() != sessionID {
		t.Fatalf("create replay: %v %s", err, replayed.Msg.GetSession().GetId())
	}

	waitTerminal := func(t *testing.T, input *agentv1.AgentSessionInput) (final *agentv1.AgentTask, events []*agentv1.AgentEvent) {
		t.Helper()
		deadline := time.Now().Add(180 * time.Second)
		for time.Now().Before(deadline) {
			getRequest := connect.NewRequest(&agentv1.GetTaskRequest{TaskId: input.GetTaskId()})
			getRequest.Header().Set(identity.UserHeader, owner)
			getRequest.Header().Set(identity.DeviceHeader, device)
			response, err := tasks.GetTask(ctx, getRequest)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			task := response.Msg.GetTask()
			switch task.GetState() {
			case agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED,
				agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED,
				agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED:
				eventsRequest := connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: input.GetTaskId()})
				eventsRequest.Header().Set(identity.UserHeader, owner)
				eventsRequest.Header().Set(identity.DeviceHeader, device)
				stream, err := tasks.WatchTaskEvents(ctx, eventsRequest)
				if err != nil {
					t.Fatalf("watch events: %v", err)
				}
				for stream.Receive() {
					events = append(events, stream.Msg().GetEvent())
				}
				return task, events
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("task never reached a terminal state: %s", input.GetTaskId())
		return nil, nil
	}

	// Turn one: the model issues a real bash tool call; its file effect must
	// land in the session's private workspace on the real disk.
	submit1 := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-1", Text: "SESSION_TOOL_TURN write the evidence file",
	})
	submit1.Header().Set(identity.UserHeader, owner)
	submit1.Header().Set(identity.DeviceHeader, device)
	turn1, err := sessions.SubmitSessionInput(ctx, submit1)
	if err != nil {
		t.Fatalf("submit turn 1: %v", err)
	}
	task1, events1 := waitTerminal(t, turn1.Msg.GetInput())
	if task1.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("turn 1 terminal state: %s", task1.GetState())
	}
	var sawToolStart, sawToolDone, sawTurn1Answer bool
	for _, event := range events1 {
		switch event.GetEvent().(type) {
		case *agentv1.AgentEvent_ToolCallStarted:
			sawToolStart = true
		case *agentv1.AgentEvent_ToolCallCompleted:
			sawToolDone = true
		case *agentv1.AgentEvent_AssistantMessage:
			if strings.Contains(event.GetAssistantMessage().GetText(), "TURN1_DONE") {
				sawTurn1Answer = true
			}
		}
	}
	if !sawToolStart || !sawToolDone {
		t.Fatalf("turn 1 missed native tool events: start=%v done=%v", sawToolStart, sawToolDone)
	}
	if !sawTurn1Answer {
		t.Fatal("turn 1 never surfaced the tool round-trip answer")
	}
	evidence := filepath.Join(stateDir, sessionID, "ws", "turn1-evidence.txt")
	content, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatalf("native tool write did not land in the session workspace: %v", err)
	}
	if strings.TrimSpace(string(content)) != "native-tool-evidence" {
		t.Fatalf("evidence content: %q", string(content))
	}

	// Turn two on the SAME session: the fixture answers from history, so a
	// has_turn1=true answer proves the official runtime continued the same
	// native context instead of starting a fresh one.
	submit2 := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-2", Text: "SESSION_COUNT verify the history",
	})
	submit2.Header().Set(identity.UserHeader, owner)
	submit2.Header().Set(identity.DeviceHeader, device)
	turn2, err := sessions.SubmitSessionInput(ctx, submit2)
	if err != nil {
		t.Fatalf("submit turn 2: %v", err)
	}
	task2, events2 := waitTerminal(t, turn2.Msg.GetInput())
	if task2.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("turn 2 terminal state: %s", task2.GetState())
	}
	continued := false
	for _, event := range events2 {
		if message := event.GetAssistantMessage(); message != nil && strings.Contains(message.GetText(), "has_turn1=true") {
			continued = true
		}
	}
	if !continued {
		t.Fatal("turn 2 answer proves the native context was NOT continued (has_turn1 missing/true)")
	}

	// Turn three on the SAME session: the model calls the harness's own
	// read-only WorkOS tool. The tool result must carry the real project
	// name created above — the proof that Harness → authorized WorkOS tool
	// → Core → real business facts works end to end (B04). The owner and
	// project never appear in the prompt: the tool derives them from the
	// session child environment.
	submit3 := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-3", Text: "SESSION_WORKOS_INFO report the project facts",
	})
	submit3.Header().Set(identity.UserHeader, owner)
	submit3.Header().Set(identity.DeviceHeader, device)
	turn3, err := sessions.SubmitSessionInput(ctx, submit3)
	if err != nil {
		t.Fatalf("submit turn 3: %v", err)
	}
	task3, events3 := waitTerminal(t, turn3.Msg.GetInput())
	if task3.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("turn 3 terminal state: %s", task3.GetState())
	}
	var workosCallID string
	var workosToolDone *agentv1.ToolCallCompleted
	sawWorkosAnswer := false
	for _, event := range events3 {
		if started := event.GetToolCallStarted(); started != nil && started.GetToolName() == "workos_project_info" {
			workosCallID = started.GetToolCallId()
		}
		if completed := event.GetToolCallCompleted(); completed != nil && completed.GetToolCallId() == workosCallID && workosCallID != "" {
			workosToolDone = completed
		}
		if message := event.GetAssistantMessage(); message != nil && strings.Contains(message.GetText(), "PROJECT_INFO:") {
			sawWorkosAnswer = true
		}
	}
	if workosToolDone == nil {
		t.Fatal("turn 3 never executed the workos_project_info tool")
	}
	if !workosToolDone.GetSuccess() {
		t.Fatalf("workos_project_info failed: %s", workosToolDone.GetOutput().GetFields()["text"].GetStringValue())
	}
	toolText := workosToolDone.GetOutput().GetFields()["text"].GetStringValue()
	if !strings.Contains(toolText, project.Msg.GetProject().GetName()) {
		t.Fatalf("workos tool result lacks the real project name %q: %s", project.Msg.GetProject().GetName(), toolText)
	}
	if !sawWorkosAnswer {
		t.Fatal("turn 3 never surfaced the WorkOS tool round-trip answer")
	}

	// Input replay: the same client key returns the recorded input, never a
	// second execution.
	replay := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-2", Text: "SESSION_COUNT verify the history",
	})
	replay.Header().Set(identity.UserHeader, owner)
	replay.Header().Set(identity.DeviceHeader, device)
	replayedInput, err := sessions.SubmitSessionInput(ctx, replay)
	if err != nil || replayedInput.Msg.GetInput().GetId() != turn2.Msg.GetInput().GetId() {
		t.Fatalf("input replay: %v", err)
	}
	// Same key with different text conflicts.
	conflict := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-2", Text: "SESSION_COUNT different text",
	})
	conflict.Header().Set(identity.UserHeader, owner)
	conflict.Header().Set(identity.DeviceHeader, device)
	if _, err := sessions.SubmitSessionInput(ctx, conflict); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("conflicting replay accepted: %v", err)
	}

	// Close forbids new inputs.
	closeRequest := connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: sessionID})
	closeRequest.Header().Set(identity.UserHeader, owner)
	closeRequest.Header().Set(identity.DeviceHeader, device)
	if _, err := sessions.CloseSession(ctx, closeRequest); err != nil {
		t.Fatalf("close: %v", err)
	}
	afterClose := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "turn-4", Text: "SESSION_COUNT after close",
	})
	afterClose.Header().Set(identity.UserHeader, owner)
	afterClose.Header().Set(identity.DeviceHeader, device)
	if _, err := sessions.SubmitSessionInput(ctx, afterClose); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("input after close accepted: %v", err)
	}
}
