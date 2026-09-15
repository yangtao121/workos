//go:build integration && harnesssessiongate

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

// TestHarnessSessionRestartRecovery proves the A05 restart semantics: after
// the gate restarts workos-core and harness-host, the closed session's input
// history and lifecycle event log are complete and readable through the same
// public AgentSessionService, every recorded input still points at exactly
// its recorded task and terminal state (no duplicate executions were driven
// by the restart), the close verdict survives, and a brand-new session on the
// same project executes a fresh native turn — the recovered stack proof.
func TestHarnessSessionRestartRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	gatewayURL := harnessSessionEnv(t, "URL")
	owner := harnessSessionEnv(t, "OWNER")
	device := harnessSessionEnv(t, "DEVICE")
	sessionID := harnessSessionEnv(t, "SESSION_ID")
	projectID := harnessSessionEnv(t, "PROJECT_ID")

	run := fmt.Sprintf("%d", time.Now().UnixNano())
	sessions := agentv1connect.NewAgentSessionServiceClient(client, gatewayURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, gatewayURL)

	// The restarted Core must still list the closed session (include_closed)
	// with its terminal state.
	listRequest := connect.NewRequest(&agentv1.ListSessionsRequest{
		ProjectId: projectID, IncludeClosed: true,
	})
	listRequest.Header().Set(identity.UserHeader, owner)
	listRequest.Header().Set(identity.DeviceHeader, device)
	listed, err := sessions.ListSessions(ctx, listRequest)
	if err != nil {
		t.Fatalf("list sessions after restart: %v", err)
	}
	var recovered *agentv1.AgentSession
	for _, session := range listed.Msg.GetSessions() {
		if session.GetId() == sessionID {
			recovered = session
		}
	}
	if recovered == nil {
		t.Fatalf("closed session %s missing from include_closed listing", sessionID)
	}
	if recovered.GetState() != agentv1.AgentSessionState_AGENT_SESSION_STATE_CLOSED {
		t.Fatalf("session state after restart: %s", recovered.GetState())
	}

	// The input history is complete: exactly the three accepted turns, each
	// with its recorded task and terminal state. The turn-2 replay returned
	// the recorded input (not a second execution) and turn-4 was refused
	// after close, so neither may appear as a new input.
	inputsRequest := connect.NewRequest(&agentv1.ListSessionInputsRequest{SessionId: sessionID, Limit: 200})
	inputsRequest.Header().Set(identity.UserHeader, owner)
	inputsRequest.Header().Set(identity.DeviceHeader, device)
	inputsResponse, err := sessions.ListSessionInputs(ctx, inputsRequest)
	if err != nil {
		t.Fatalf("list session inputs after restart: %v", err)
	}
	inputs := inputsResponse.Msg.GetInputs()
	if len(inputs) != 3 {
		t.Fatalf("expected exactly the three recorded inputs after restart, got %d", len(inputs))
	}
	seenKeys := make(map[string]bool)
	for _, input := range inputs {
		if seenKeys[input.GetClientInputId()] {
			t.Fatalf("duplicate input key after restart: %s", input.GetClientInputId())
		}
		seenKeys[input.GetClientInputId()] = true
		if input.GetTaskId() == "" {
			t.Fatalf("input %s lost its task binding", input.GetClientInputId())
		}
		if input.GetState() != agentv1.AgentSessionInputState_AGENT_SESSION_INPUT_STATE_COMPLETED {
			t.Fatalf("input %s terminal state drifted: %s", input.GetClientInputId(), input.GetState())
		}
		// The recorded task is still readable and still terminal — the
		// restart did not re-execute or reset it.
		taskRequest := connect.NewRequest(&agentv1.GetTaskRequest{TaskId: input.GetTaskId()})
		taskRequest.Header().Set(identity.UserHeader, owner)
		taskRequest.Header().Set(identity.DeviceHeader, device)
		taskResponse, err := tasks.GetTask(ctx, taskRequest)
		if err != nil {
			t.Fatalf("get recorded task %s: %v", input.GetTaskId(), err)
		}
		if taskResponse.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
			t.Fatalf("recorded task %s state drifted after restart: %s", input.GetTaskId(), taskResponse.Msg.GetTask().GetState())
		}
	}
	for _, key := range []string{"turn-1", "turn-2", "turn-3"} {
		if !seenKeys[key] {
			t.Fatalf("input %s missing after restart", key)
		}
	}
	if seenKeys["turn-4"] {
		t.Fatal("the post-close refused input must never appear as an accepted input")
	}

	// One recorded task's event stream replays after the restart: the
	// completed turn's events stay readable without any model call.
	eventsRequest := connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: inputs[0].GetTaskId()})
	eventsRequest.Header().Set(identity.UserHeader, owner)
	eventsRequest.Header().Set(identity.DeviceHeader, device)
	stream, err := tasks.WatchTaskEvents(ctx, eventsRequest)
	if err != nil {
		t.Fatalf("watch recorded task events: %v", err)
	}
	replayed := 0
	sawTerminal := false
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		replayed++
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			sawTerminal = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("recorded task event stream: %v", err)
	}
	if replayed == 0 || !sawTerminal {
		t.Fatalf("recorded task events unreadable after restart: %d events, terminal=%v", replayed, sawTerminal)
	}

	// The session lifecycle log replays from cursor zero: every accepted
	// input dispatched and terminated exactly once, and the close is the
	// final state change.
	lifecycleRequest := connect.NewRequest(&agentv1.WatchSessionEventsRequest{SessionId: sessionID})
	lifecycleRequest.Header().Set(identity.UserHeader, owner)
	lifecycleRequest.Header().Set(identity.DeviceHeader, device)
	lifecycle, err := sessions.WatchSessionEvents(ctx, lifecycleRequest)
	if err != nil {
		t.Fatalf("watch session events after restart: %v", err)
	}
	accepted, dispatched, terminal := make(map[string]int), make(map[string]int), make(map[string]int)
	closed := false
	for lifecycle.Receive() {
		for _, sessionEvent := range lifecycle.Msg().GetEvents() {
			switch event := sessionEvent.GetEvent().(type) {
			case *agentv1.AgentSessionEvent_InputAccepted:
				accepted[event.InputAccepted.GetInputId()]++
			case *agentv1.AgentSessionEvent_InputDispatched:
				dispatched[event.InputDispatched.GetInputId()]++
			case *agentv1.AgentSessionEvent_InputTerminal:
				terminal[event.InputTerminal.GetInputId()]++
				if event.InputTerminal.GetTerminalState() != agentv1.AgentSessionInputState_AGENT_SESSION_INPUT_STATE_COMPLETED {
					t.Fatalf("terminal event for %s drifted: %s", event.InputTerminal.GetInputId(), event.InputTerminal.GetTerminalState())
				}
			case *agentv1.AgentSessionEvent_StateChanged:
				if event.StateChanged.GetCurrent() == agentv1.AgentSessionState_AGENT_SESSION_STATE_CLOSED {
					closed = true
				}
			}
		}
	}
	if err := lifecycle.Err(); err != nil {
		t.Fatalf("session lifecycle stream: %v", err)
	}
	if len(terminal) != 3 {
		t.Fatalf("expected terminal events for exactly three inputs, got %d", len(terminal))
	}
	for _, input := range inputs {
		id := input.GetId()
		if accepted[id] != 1 || dispatched[id] != 1 || terminal[id] != 1 {
			t.Fatalf("input %s lifecycle drift: accepted=%d dispatched=%d terminal=%d", input.GetClientInputId(), accepted[id], dispatched[id], terminal[id])
		}
	}
	if !closed {
		t.Fatal("session lifecycle log never recorded the closed state")
	}

	// The close verdict is still enforced after the restart.
	refused := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: sessionID, ClientInputId: "recovery-after-close", Text: "SESSION_COUNT must be refused",
	})
	refused.Header().Set(identity.UserHeader, owner)
	refused.Header().Set(identity.DeviceHeader, device)
	if _, err := sessions.SubmitSessionInput(ctx, refused); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("input after close+restart accepted: %v", err)
	}

	// The stack recovered: a NEW session on the same project runs a fresh
	// native turn on the restarted harness host. The fixture answers from
	// the new session's history (has_turn1=false), proving a genuinely new
	// native context — not a replay of the old one.
	create := connect.NewRequest(&agentv1.CreateSessionRequest{
		ProjectId: projectID, IdempotencyKey: "harness-session-recovery-" + run,
	})
	create.Header().Set(identity.UserHeader, owner)
	create.Header().Set(identity.DeviceHeader, device)
	var created *connect.Response[agentv1.CreateSessionResponse]
	for attempt := 0; attempt < 30; attempt++ {
		created, err = sessions.CreateSession(ctx, create)
		if err == nil {
			break
		}
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("create recovery session: %v", err)
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("create recovery session after catalog retry: %v", err)
	}
	newSession := created.Msg.GetSession().GetId()
	defer func() {
		closeRequest := connect.NewRequest(&agentv1.CloseSessionRequest{SessionId: newSession})
		closeRequest.Header().Set(identity.UserHeader, owner)
		closeRequest.Header().Set(identity.DeviceHeader, device)
		_, _ = sessions.CloseSession(context.Background(), closeRequest)
	}()

	submit := connect.NewRequest(&agentv1.SubmitSessionInputRequest{
		SessionId: newSession, ClientInputId: "recovery-turn-1", Text: "SESSION_COUNT fresh session after restart",
	})
	submit.Header().Set(identity.UserHeader, owner)
	submit.Header().Set(identity.DeviceHeader, device)
	turn, err := sessions.SubmitSessionInput(ctx, submit)
	if err != nil {
		t.Fatalf("submit recovery turn: %v", err)
	}
	deadline := time.Now().Add(180 * time.Second)
	var task *agentv1.AgentTask
	for time.Now().Before(deadline) {
		taskRequest := connect.NewRequest(&agentv1.GetTaskRequest{TaskId: turn.Msg.GetInput().GetTaskId()})
		taskRequest.Header().Set(identity.UserHeader, owner)
		taskRequest.Header().Set(identity.DeviceHeader, device)
		taskResponse, err := tasks.GetTask(ctx, taskRequest)
		if err != nil {
			t.Fatalf("get recovery task: %v", err)
		}
		task = taskResponse.Msg.GetTask()
		if task.GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED ||
			task.GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED ||
			task.GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if task.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("recovery turn terminal state: %s", task.GetState())
	}
	freshEventsRequest := connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: turn.Msg.GetInput().GetTaskId()})
	freshEventsRequest.Header().Set(identity.UserHeader, owner)
	freshEventsRequest.Header().Set(identity.DeviceHeader, device)
	freshStream, err := tasks.WatchTaskEvents(ctx, freshEventsRequest)
	if err != nil {
		t.Fatalf("watch recovery events: %v", err)
	}
	freshAnswer := false
	var recoveryText string
	for freshStream.Receive() {
		if message := freshStream.Msg().GetEvent().GetAssistantMessage(); message != nil {
			recoveryText += message.GetText()
			// The fixture answers from the new session's request history:
			// the old session's turn-1 marker must be absent (has_turn1=
			// false) — the restarted harness host executed a genuinely new
			// native context, not a replay of the old one. The absolute
			// message count is a runtime detail (injected context rows) and
			// is not asserted.
			if strings.Contains(message.GetText(), "has_turn1=false") {
				freshAnswer = true
			}
		}
	}
	if err := freshStream.Err(); err != nil {
		t.Fatalf("recovery event stream: %v", err)
	}
	if !freshAnswer {
		t.Fatalf("recovery turn did not prove a fresh native context on the restarted harness host, answer: %q", recoveryText)
	}
	if strings.Contains(recoveryText, "has_turn1=true") {
		t.Fatalf("recovery turn answered from stale history: %q", recoveryText)
	}
}
