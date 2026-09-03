package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

// The child protocol is line-delimited JSON-RPC 2.0 (ADR-0015). Request
// methods: initialize, task/new, task/cancel. The only server-to-client
// notification is task/event; its ordered event kinds are validated against
// the canonical event stream below — anything else fails the run closed.
const (
	methodInitialize = "initialize"
	methodTaskNew    = "task/new"

	notifyTaskEvent = "task/event"

	eventStarted   = "started"
	eventDelta     = "delta"
	eventMessage   = "message"
	eventUsage     = "usage"
	eventCompleted = "completed"
	eventFailed    = "failed"
)

// maxEventBytes bounds one protocol line; a larger line is transport damage
// and fails the run instead of truncating silently.
const maxEventBytes = 1024 * 1024

// maxDeltas bounds a single run's delta stream: the fixture contract is a
// bounded review narration, not an unbounded pipe.
const maxDeltas = 4096

type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int            `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	Params  struct {
		Event protocolEvent `json:"event"`
	} `json:"params"`
}

type protocolEvent struct {
	Kind         string `json:"kind"`
	RunID        string `json:"runId"`
	Seq          int    `json:"seq"`
	Text         string `json:"text"`
	Delta        string `json:"delta"`
	Provider     string `json:"provider"`
	OutputTokens int64  `json:"outputTokens"`
	Reason       string `json:"reason"`
}

// execute drives one app-server child for one task: initialize, task/new,
// then the validated event mapping loop. Every failure path kills the child
// and returns before the canonical stream can advance past its verdict.
func (p *Provider) execute(ctx context.Context, taskID, runID string, prepared preparedInput, lease *ports.CredentialLease, emit ports.Emit) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, p.config.AppServer)
	command.Env = []string{
		// The allowlist is exact: the lease secret and the child's minimal
		// runtime facts. Nothing else from the parent environment crosses.
		credentialEnv + "=" + string(lease.Secret),
		"CODEX_FIXTURE_MODE=" + p.config.fixtureMode,
	}
	command.Env = append(command.Env, p.config.fixtureEnv...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open codex stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open codex stdout: %w", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return ports.NewRunError(ports.ErrorKindConfiguration, "codex app server is unavailable", false, err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	send := func(request rpcRequest) error {
		encoded, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("encode codex request: %w", err)
		}
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("write codex request: %w", err)
		}
		return nil
	}
	nextID := 0
	nextID++
	if err := send(rpcRequest{JSONRPC: "2.0", ID: nextID, Method: methodInitialize, Params: map[string]any{
		"protocolVersion": protocolVersion,
		"clientInfo":      map[string]string{"name": "workos-harness-codex", "version": AdapterVersion},
	}}); err != nil {
		return err
	}

	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 64*1024), maxEventBytes)
	readMessage := func() (*rpcMessage, error) {
		if !reader.Scan() {
			if err := reader.Err(); err != nil {
				return nil, fmt.Errorf("read codex output: %w", err)
			}
			if ctx.Err() != nil {
				return nil, ports.NewRunError(ports.ErrorKindTimeout, "codex run exceeded its runtime deadline", true, ctx.Err())
			}
			return nil, errors.New("codex app server closed its output before a terminal event")
		}
		var message rpcMessage
		if err := json.Unmarshal(reader.Bytes(), &message); err != nil {
			return nil, fmt.Errorf("decode codex output: %w", err)
		}
		return &message, nil
	}

	// The initialize response is the protocol version handshake: a drift is
	// a hard protocol failure, never a best-effort downgrade.
	handshake, err := readMessage()
	if err != nil {
		return err
	}
	if handshake.Error != nil || handshake.ID == nil || *handshake.ID != nextID {
		return ports.NewRunError(ports.ErrorKindProtocol, "codex initialize handshake failed", false, nil)
	}
	var handshakeResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(handshake.Result, &handshakeResult); err != nil || handshakeResult.ProtocolVersion != protocolVersion {
		return ports.NewRunError(ports.ErrorKindProtocol, "codex app server speaks an unsupported protocol version", false, nil)
	}

	nextID++
	if err := send(rpcRequest{JSONRPC: "2.0", ID: nextID, Method: methodTaskNew, Params: map[string]any{
		"taskId": taskID, "goal": prepared.goal, "maxTokens": prepared.maxTokens,
	}}); err != nil {
		return err
	}
	// The app server owns the protocol-level run identity: adopt the exact
	// runId from the task/new response and bind every canonical event to it.
	newResponse, err := readMessage()
	if err != nil {
		return err
	}
	if newResponse.Error != nil || newResponse.ID == nil || *newResponse.ID != nextID {
		return ports.NewRunError(ports.ErrorKindProtocol, "codex task/new was rejected", false, nil)
	}
	var created struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(newResponse.Result, &created); err != nil || created.RunID == "" {
		return ports.NewRunError(ports.ErrorKindProtocol, "codex task/new returned no run identity", false, nil)
	}
	runID = created.RunID

	started := newTaskEvents(emit, runID)
	for {
		message, err := readMessage()
		if err != nil {
			return err
		}
		if message.Error != nil {
			return ports.NewRunError(ports.ErrorKindProvider, "codex app server rejected the task", false, nil)
		}
		if message.ID != nil {
			// The task/new response itself carries no facts we need; only
			// events advance the run.
			continue
		}
		if message.Method != notifyTaskEvent {
			return ports.NewRunError(ports.ErrorKindProtocol, "codex emitted an unknown notification", false, nil)
		}
		if verdict, err := started.canonical(&message.Params.Event); err != nil {
			return err
		} else if verdict {
			return nil
		}
	}
}

// taskEventMapper turns validated protocol events into exactly one ordered
// canonical stream. It enforces: started first, nothing after a terminal,
// at most one usage within budget, and adapter-owned metadata only.
type taskEventMapper struct {
	emit     ports.Emit
	runID    string
	sequence int
	sawStart bool
	deltas   int
	usage    int64
	sawUsage bool
	done     bool
}

func newTaskEvents(emit ports.Emit, runID string) *taskEventMapper {
	return &taskEventMapper{emit: emit, runID: runID}
}

// canonical maps one protocol event; a true verdict means the run reached
// its terminal and the canonical stream is complete.
func (m *taskEventMapper) canonical(event *protocolEvent) (bool, error) {
	if m.done {
		return false, ports.NewRunError(ports.ErrorKindProtocol, "codex emitted an event after the terminal", false, nil)
	}
	if event.RunID != m.runID {
		return false, ports.NewRunError(ports.ErrorKindProtocol, "codex event bound to a foreign run", false, nil)
	}
	m.sequence++
	if event.Seq != m.sequence {
		return false, ports.NewRunError(ports.ErrorKindProtocol, "codex event stream is out of order", false, nil)
	}
	switch event.Kind {
	case eventStarted:
		if m.sawStart {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex restarted an already started run", false, nil)
		}
		m.sawStart = true
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{
			RunId: m.runID, ProviderId: ProviderID,
		}}}); err != nil {
			return false, err
		}
		return false, nil
	case eventDelta:
		if !m.sawStart || m.deltas >= maxDeltas || len(event.Delta) == 0 {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex emitted an invalid delta", false, nil)
		}
		m.deltas++
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{
			Text: event.Delta,
		}}}); err != nil {
			return false, err
		}
		return false, nil
	case eventMessage:
		if !m.sawStart || strings.TrimSpace(event.Text) == "" {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex emitted an invalid assistant message", false, nil)
		}
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{
			Text: event.Text,
		}}}); err != nil {
			return false, err
		}
		return false, nil
	case eventUsage:
		if !m.sawStart || m.sawUsage || event.OutputTokens < 0 || event.OutputTokens > m.usageCeiling() {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex reported invalid usage", false, nil)
		}
		m.sawUsage = true
		m.usage = event.OutputTokens
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_UsageRecorded{UsageRecorded: &agentv1.UsageRecorded{
			OutputTokens: event.OutputTokens, Model: "codex-fixture",
		}}}); err != nil {
			return false, err
		}
		return false, nil
	case eventCompleted:
		if !m.sawStart {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex completed a run that never started", false, nil)
		}
		m.done = true
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{
			Summary: "Task completed by Codex Harness",
		}}}); err != nil {
			return false, err
		}
		return true, nil
	case eventFailed:
		if !m.sawStart {
			return false, ports.NewRunError(ports.ErrorKindProtocol, "codex failed a run that never started", false, nil)
		}
		m.done = true
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "codex run failed"
		}
		if err := m.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunFailed{RunFailed: &agentv1.RunFailed{
			Reason: reason,
		}}}); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, ports.NewRunError(ports.ErrorKindProtocol, "codex emitted an unknown event kind", false, nil)
	}
}

// usageCeiling is the enforced per-run token maximum the adapter advertises;
// the fixture (and the pinned real runtime) never reports beyond it.
func (m *taskEventMapper) usageCeiling() int64 { return MaximumMaxTokens }
