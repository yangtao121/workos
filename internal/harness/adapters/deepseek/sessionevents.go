package deepseek

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// sessionEventMapper maps the native event stream of one continuous-session
// turn onto canonical AgentEvents (ADR-0030). Unlike the single-shot
// streamState it accepts tool traffic: tool calls surface as
// ToolCallStarted/ToolCallCompleted with structured inputs and outputs.
type sessionEventMapper struct {
	sessionID  string
	model      string
	answer     bytes.Buffer
	usages     map[string]tokenUsage
	emit       ports.Emit
	sawActive  bool
	idle       bool
	turnEnded  bool
	turnReason turnEndReason
	failure    llmFailure
}

func (s *sessionEventMapper) handleNotification(envelope rpcEnvelope) error {
	switch envelope.Method {
	case "session.event":
		var params sessionEventParams
		if err := json.Unmarshal(envelope.Params, &params); err != nil || params.SessionID != s.sessionID || params.Event.Type == "" {
			return protocolError("DeepSeek Harness session event is malformed", err)
		}
		return s.handleSessionEvent(params.Event.Type, params.Event.Data)
	case "session.status":
		var params sessionStatusParams
		if err := json.Unmarshal(envelope.Params, &params); err != nil || params.SessionID != s.sessionID {
			return protocolError("DeepSeek Harness session status is malformed", err)
		}
		status, err := decodeStatus(params.Status)
		if err != nil {
			return protocolError(err.Error(), err)
		}
		switch strings.ToLower(status) {
		case "running", "active", "busy":
			s.sawActive, s.idle = true, false
		case "idle":
			if s.sawActive || s.turnEnded {
				s.idle = true
			}
		case "starting", "initializing", "shutting-down", "stopped":
		default:
			return protocolError("DeepSeek Harness emitted an unknown session status", nil)
		}
		return nil
	case "subagent.started", "subagent.finished":
		return protocolError("DeepSeek Harness attempted an unsupported subagent operation", nil)
	default:
		return protocolError("DeepSeek Harness emitted an unknown notification", nil)
	}
}

func (s *sessionEventMapper) handleSessionEvent(eventType string, raw json.RawMessage) error {
	switch eventType {
	case "assistant/chunk":
		var data struct {
			Turn  int64 `json:"turn"`
			Step  int64 `json:"step"`
			Chunk struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				BlockType string          `json:"blockType"`
				Block     json.RawMessage `json:"block"`
				Usage     json.RawMessage `json:"usage"`
			} `json:"chunk"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || data.Chunk.Type == "" {
			return protocolError("DeepSeek Harness assistant chunk is malformed", err)
		}
		switch data.Chunk.Type {
		case "text-delta":
			if data.Chunk.Text == "" {
				return nil
			}
			if s.answer.Len()+len(data.Chunk.Text) > maximumAnswerBytes {
				return protocolError("DeepSeek Harness response exceeded the answer limit", nil)
			}
			if err := s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{Text: data.Chunk.Text}}}); err != nil {
				return err
			}
			_, _ = s.answer.WriteString(data.Chunk.Text)
		case "usage":
			if err := s.recordUsage(data.Turn, data.Step, data.Chunk.Usage); err != nil {
				return err
			}
		case "block-start":
			if data.Chunk.BlockType != "text" && data.Chunk.BlockType != "reasoning" && data.Chunk.BlockType != "tool-call" {
				return protocolError("DeepSeek Harness attempted an unsupported content block", nil)
			}
		case "block-end":
			var block struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data.Chunk.Block, &block); err != nil || (block.Type != "text" && block.Type != "reasoning" && block.Type != "tool-call") {
				return protocolError("DeepSeek Harness attempted an unsupported content block", err)
			}
			// A tool-call block-end carries no canonical fact: the
			// authoritative call arrives as the tool/call event.
		case "reasoning-delta", "finish", "tool-call-delta":
			// Reasoning is intentionally not exposed; finish is superseded by
			// turn/end; streamed tool arguments are superseded by tool/call.
		default:
			return protocolError("DeepSeek Harness emitted an unknown assistant chunk", nil)
		}
		return nil
	case "assistant/message":
		var data struct {
			Turn    int64 `json:"turn"`
			Step    int64 `json:"step"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || data.Message.Role != "assistant" {
			return protocolError("DeepSeek Harness assistant message is malformed", err)
		}
		var committed bytes.Buffer
		for _, block := range data.Message.Content {
			switch block.Type {
			case "text":
				if committed.Len()+len(block.Text) > maximumAnswerBytes {
					return protocolError("DeepSeek Harness response exceeded the answer limit", nil)
				}
				_, _ = committed.WriteString(block.Text)
			case "reasoning", "tool-call":
			default:
				return protocolError("DeepSeek Harness attempted an unsupported content block", nil)
			}
		}
		s.answer = committed
		if len(data.Usage) != 0 && string(data.Usage) != "null" {
			return s.recordUsage(data.Turn, data.Step, data.Usage)
		}
		return nil
	case "tool/call":
		var data struct {
			CallID    string `json:"callId"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || data.CallID == "" || data.Name == "" {
			return protocolError("DeepSeek Harness tool call is malformed", err)
		}
		input := &structpb.Struct{}
		if data.Arguments != "" {
			if err := protojson.Unmarshal([]byte(data.Arguments), input); err != nil {
				return protocolError("DeepSeek Harness tool call arguments are malformed", err)
			}
		}
		return s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStarted{
			ToolCallId: data.CallID, ToolName: data.Name, Input: input,
		}}})
	case "tool/result":
		var data struct {
			Message struct {
				Source struct {
					CallID string `json:"callId"`
				} `json:"source"`
				Content []struct {
					Type    string `json:"type"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || data.Message.Source.CallID == "" {
			return protocolError("DeepSeek Harness tool result is malformed", err)
		}
		var text string
		for _, block := range data.Message.Content {
			if block.Type != "tool-result" {
				return protocolError("DeepSeek Harness attempted an unsupported tool result block", nil)
			}
			for _, part := range block.Content {
				if part.Type != "text" {
					return protocolError("DeepSeek Harness attempted an unsupported tool result part", nil)
				}
				if len(text)+len(part.Text) > maximumAnswerBytes {
					return protocolError("DeepSeek Harness response exceeded the answer limit", nil)
				}
				text += part.Text
			}
		}
		output, err := structpb.NewStruct(map[string]any{"text": text})
		if err != nil {
			return protocolError("DeepSeek Harness tool result is malformed", err)
		}
		return s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_ToolCallCompleted{ToolCallCompleted: &agentv1.ToolCallCompleted{
			ToolCallId: data.Message.Source.CallID, Success: !strings.HasPrefix(text, "Error:"), Output: output,
		}}})
	case "turn/end":
		var data struct {
			Reason turnEndReason `json:"reason"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || data.Reason.Kind == "" || s.turnEnded {
			return protocolError("DeepSeek Harness turn end event is malformed", err)
		}
		s.turnEnded, s.turnReason = true, data.Reason
		if len(data.Reason.Error) != 0 {
			s.failure = decodeFailure(data.Reason.Error)
		}
		return nil
	case "turn/start", "step/start", "step/end", "user/message", "request/header", "request/context",
		"session/title", "steering/message", "llm/retry", "llm/retry-started", "agent-preset/selected":
		return nil
	default:
		if strings.HasPrefix(eventType, "agent/inbox/") {
			return nil
		}
		if eventType == "tool/start" || eventType == "tool/end" || eventType == "subagent/start" || eventType == "subagent/end" {
			return protocolError("DeepSeek Harness attempted an unsupported tool or subagent operation", nil)
		}
		return protocolError("DeepSeek Harness emitted unsupported session event "+safeProtocolLabel(eventType), nil)
	}
}

func (s *sessionEventMapper) recordUsage(turn, step int64, raw json.RawMessage) error {
	var usage tokenUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return protocolError("DeepSeek Harness token usage is malformed", err)
	}
	if _, ok := addTokens(usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens); !ok || usage.OutputTokens < 0 {
		return protocolError("DeepSeek Harness reported invalid token usage", nil)
	}
	s.usages[strconv.FormatInt(turn, 10)+":"+strconv.FormatInt(step, 10)] = usage
	return nil
}

func (s *sessionEventMapper) finishTurn() error {
	switch strings.ToLower(strings.ReplaceAll(s.turnReason.Kind, "_", "-")) {
	case "completed", "max-tokens":
		return nil
	case "error", "blocked":
		return classifyFailure(s.failure)
	case "aborted", "interrupted":
		return ports.NewRunError(ports.ErrorKindTransport, "DeepSeek Harness run was interrupted", true, nil)
	default:
		return protocolError("DeepSeek Harness returned an unknown turn result", nil)
	}
}

// emitTurnSummary publishes the bounded turn-end facts in the canonical
// order: one assistant message, one usage record, one completion.
func (s *sessionEventMapper) emitTurnSummary() error {
	if s.answer.Len() != 0 {
		if err := s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: s.answer.String()}}}); err != nil {
			return err
		}
	}
	if len(s.usages) != 0 {
		var inputTokens, outputTokens int64
		for _, usage := range s.usages {
			stepInput, ok := addTokens(usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			if !ok || usage.OutputTokens < 0 {
				return protocolError("DeepSeek Harness reported invalid token usage", nil)
			}
			inputTokens, ok = addTokens(inputTokens, stepInput)
			if !ok {
				return protocolError("DeepSeek Harness reported invalid token usage", nil)
			}
			outputTokens, ok = addTokens(outputTokens, usage.OutputTokens)
			if !ok {
				return protocolError("DeepSeek Harness reported invalid token usage", nil)
			}
		}
		if err := s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_UsageRecorded{UsageRecorded: &agentv1.UsageRecorded{
			InputTokens: inputTokens, OutputTokens: outputTokens, Model: s.model,
		}}}); err != nil {
			return err
		}
	}
	return s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{
		Summary: "Session turn completed",
	}}})
}
