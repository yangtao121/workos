package deepseek

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/harness/ports"
)

// TestSessionApprovalEventsFailClosed pins the A09 honesty contract of the
// pinned runtime (0.1.1rc1): its event vocabulary contains approval/asked and
// approval/decided, but the sdk-jsonrpc-server wire dispatches only
// initialize / session/prompt / shutdown — there is NO approval-response
// method WorkOS could call. So an approval-style session event must fail the
// turn closed with a non-retryable protocol error instead of being silently
// swallowed (which would read as an implicit approval or let the child hang),
// and the generated composition must keep the runtime's own approval policy
// row at `policy: ask` so no WorkOS-side escalation path is implied.
func TestSessionApprovalEventsFailClosed(t *testing.T) {
	events, emit := collectEvents()
	mapper := &sessionEventMapper{
		sessionID: testSessionID, model: DefaultModel, emit: emit,
		usages: map[string]tokenUsage{},
	}
	for _, eventType := range []string{"approval/asked", "approval/decided", "session/request_permission"} {
		params, err := json.Marshal(map[string]any{
			"sessionId": testSessionID,
			"event":     map[string]any{"type": eventType, "data": map[string]any{"reason": "run a command"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		err = mapper.handleNotification(rpcEnvelope{JSONRPC: "2.0", Method: "session.event", Params: params})
		var runErr *ports.RunError
		if !errors.As(err, &runErr) || runErr.Kind != ports.ErrorKindProtocol || runErr.Retryable {
			t.Fatalf("%s must fail closed as a non-retryable protocol error, got: %v", eventType, err)
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s error must name the unsupported event: %v", eventType, err)
		}
	}
	if len(*events) != 0 {
		t.Fatalf("an approval event must not emit canonical events: %#v", *events)
	}

	// The composition the adapter generates keeps the official approval row
	// at policy: ask — B04's read-only WorkOS toolset cannot trigger or
	// satisfy an approval escalation, and no row is dropped to pretend
	// approvals away.
	composition := string(renderCordisConfig(t.TempDir(), "/tmp/workos-ws", validConfig(t, "stream")))
	approvalRow := strings.Index(composition, "- id: approval\n")
	if approvalRow < 0 {
		t.Fatal("the generated composition dropped the official approval row")
	}
	window := composition[approvalRow:]
	if next := strings.Index(window[1:], "- id: "); next >= 0 {
		window = window[:next+1]
	}
	if !strings.Contains(window, "policy: ask") {
		t.Fatalf("approval row must keep policy ask, got:\n%s", window)
	}
}
