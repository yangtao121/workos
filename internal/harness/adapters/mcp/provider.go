// Package mcp adapts a local MCP stdio server to the canonical harness
// provider contract (ADR-0015). The MCP protocol details stay inside this
// package. The capability subset is deliberately the degraded Generic-CLI
// shape — no streaming, no usage, no hard budgets, no structured artifacts,
// no context refs, no credential lease — and every run enforces bounded
// output, bounded time, and a deterministic terminal verdict.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

// maxLineBytes bounds one protocol line; a larger line is transport damage
// and fails the run instead of truncating silently.
const maxLineBytes = 1024 * 1024

// Provider is the MCP harness adapter.
type Provider struct {
	config Config

	mu     sync.RWMutex
	health commonv1.HealthState
	reason string
}

func New(config Config) *Provider {
	config = normalizeConfig(config)
	provider := &Provider{config: config}
	if err := validateConfig(config); err != nil {
		provider.health = commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
		provider.reason = err.Error()
	} else {
		provider.health = commonv1.HealthState_HEALTH_STATE_HEALTHY
	}
	return provider
}

// Describe declares the honest MCP subset: nothing beyond a bounded
// blocking tool call is claimed, so App-run budget requirements fail closed
// at the Task Router exactly like the Generic CLI.
func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	p.mu.RLock()
	health, reason := p.health, p.reason
	p.mu.RUnlock()
	return &harnessv1.HarnessProviderInfo{
		Id:                ProviderID,
		DisplayName:       "MCP Server Harness",
		AdapterVersion:    AdapterVersion,
		Health:            health,
		UnavailableReason: reason,
		Capabilities:      &harnessv1.HarnessCapabilities{
			// Deliberately false across the board: the degraded MCP subset
			// streams nothing, reports no usage, and enforces no provider
			// budgets. Claiming more would let unbudgeted App runs queue.
		},
	}
}

func (p *Provider) setHealth(health commonv1.HealthState, reason string) {
	p.mu.Lock()
	p.health, p.reason = health, reason
	p.mu.Unlock()
}

// Run executes one canonical task against the MCP server.
func (p *Provider) Run(ctx context.Context, execution ports.Execution) error {
	taskID := execution.TaskID
	_ = execution.Artifacts
	if err := validateConfig(p.config); err != nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, err.Error())
		return ports.NewRunError(ports.ErrorKindConfiguration, err.Error(), false, nil)
	}
	// No credential path exists in this adapter: a lease attached to its
	// execution is a protocol violation, never silently ignored.
	if execution.Credential != nil {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness does not accept credential leases", false, nil)
	}
	if len(execution.Context) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness does not accept resolved context", false, nil)
	}
	input := execution.Input
	if input == nil {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp task input is required", false, nil)
	}
	goal := input.GetGoal()
	if strings.TrimSpace(goal) == "" {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp task goal is required", false, nil)
	}
	if len(goal) > maximumGoalBytes {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp task goal exceeds the supported size", false, nil)
	}
	if role := input.GetRole(); role != "" && role != "general" {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness supports only the general role", false, nil)
	}
	if len(input.GetRequestedCapabilities()) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness does not support requested capabilities", false, nil)
	}
	if len(input.GetOutputArtifactTypes()) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness does not support structured artifacts", false, nil)
	}
	if input.GetBudget() != nil && (input.GetBudget().GetMaxTokens() != 0 || input.GetBudget().GetMaxCostDecimal() != "" || input.GetBudget().GetMaxRuntimeSeconds() != 0) {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "mcp harness does not enforce provider budgets", false, nil)
	}

	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	err := p.execute(ctx, taskID, goal, execution.Emit)
	if err == nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_HEALTHY, "")
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var runErr *ports.RunError
	if errors.As(err, &runErr) {
		switch runErr.Kind {
		case ports.ErrorKindConfiguration:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, runErr.Error())
		case ports.ErrorKindProvider, ports.ErrorKindProtocol, ports.ErrorKindTransport, ports.ErrorKindTimeout:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_DEGRADED, runErr.Error())
		}
	}
	return err
}

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
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (p *Provider) execute(ctx context.Context, taskID, goal string, emit ports.Emit) error {
	command := exec.CommandContext(ctx, p.config.Server, p.config.Arguments...)
	command.Env = append([]string{}, p.config.fixtureEnv...)
	if p.config.fixtureMode != "" {
		command.Env = append(command.Env, "MCP_FIXTURE_MODE="+p.config.fixtureMode)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open mcp stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open mcp stdout: %w", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return ports.NewRunError(ports.ErrorKindConfiguration, "mcp server is unavailable", false, err)
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
			return fmt.Errorf("encode mcp request: %w", err)
		}
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("write mcp request: %w", err)
		}
		return nil
	}
	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 64*1024), maxLineBytes)
	readResult := func(id int) (json.RawMessage, error) {
		for reader.Scan() {
			var message rpcMessage
			if err := json.Unmarshal(reader.Bytes(), &message); err != nil {
				return nil, fmt.Errorf("decode mcp output: %w", err)
			}
			if message.Error != nil {
				return nil, ports.NewRunError(ports.ErrorKindProvider, "mcp server rejected the call", false, nil)
			}
			if message.ID == nil || *message.ID != id {
				continue
			}
			return message.Result, nil
		}
		if err := reader.Err(); err != nil {
			// The scanner budget guards against one oversized protocol line.
			return nil, ports.NewRunError(ports.ErrorKindProtocol, "mcp server exceeded the bounded output budget", false, err)
		}
		if ctx.Err() != nil {
			return nil, ports.NewRunError(ports.ErrorKindTimeout, "mcp call exceeded its runtime deadline", true, ctx.Err())
		}
		return nil, errors.New("mcp server closed its output before answering")
	}

	nextID := 0
	nextID++
	if err := send(rpcRequest{JSONRPC: "2.0", ID: nextID, Method: "initialize", Params: map[string]any{
		"protocolVersion": protocolVersion,
		"clientInfo":      map[string]string{"name": "workos-harness-mcp", "version": AdapterVersion},
	}}); err != nil {
		return err
	}
	handshake, err := readResult(nextID)
	if err != nil {
		return err
	}
	var handshakeResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(handshake, &handshakeResult); err != nil || handshakeResult.ProtocolVersion != protocolVersion {
		return ports.NewRunError(ports.ErrorKindProtocol, "mcp server speaks an unsupported protocol version", false, nil)
	}

	nextID++
	if err := send(rpcRequest{JSONRPC: "2.0", ID: nextID, Method: "tools/list"}); err != nil {
		return err
	}
	toolsResult, err := readResult(nextID)
	if err != nil {
		return err
	}
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolsResult, &tools); err != nil {
		return fmt.Errorf("decode mcp tools list: %w", err)
	}
	hasTask := false
	for _, tool := range tools.Tools {
		if tool.Name == taskToolName {
			hasTask = true
		}
	}
	if !hasTask {
		return ports.NewRunError(ports.ErrorKindConfiguration, "mcp server does not expose the task tool", false, nil)
	}

	nextID++
	if err := send(rpcRequest{JSONRPC: "2.0", ID: nextID, Method: "tools/call", Params: map[string]any{
		"name": taskToolName,
		"arguments": map[string]any{
			"goal": goal, "taskId": taskID,
		},
	}}); err != nil {
		return err
	}
	callResult, err := readResult(nextID)
	if err != nil {
		return err
	}
	var call struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(callResult, &call); err != nil {
		return fmt.Errorf("decode mcp tool result: %w", err)
	}
	text := ""
	for index, block := range call.Content {
		if block.Type != "text" {
			return ports.NewRunError(ports.ErrorKindProtocol, "mcp tool returned a non-text block", false, nil)
		}
		if index == 0 {
			text = block.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		return ports.NewRunError(ports.ErrorKindProtocol, "mcp tool returned no content", false, nil)
	}

	if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{
		RunId: "mcp-" + taskID, ProviderId: ProviderID,
	}}}); err != nil {
		return err
	}
	if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{
		Text: text,
	}}}); err != nil {
		return err
	}
	return emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{
		Summary: fmt.Sprintf("Task %s completed by MCP Harness", taskID),
	}}})
}
