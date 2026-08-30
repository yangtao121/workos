package deepseek

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

const (
	maximumJSONRPCLineBytes = 20 * 1024 * 1024
	maximumRuntimeBytes     = 64 * 1024 * 1024
	maximumAnswerBytes      = 16 * 1024 * 1024
	shutdownTimeout         = time.Second
)

type streamState struct {
	runID            string
	sessionID        string
	messageID        string
	pendingSplicedID string
	// structured suppresses raw text-delta emission: the model's answer is
	// the strict JSON review document, never timeline content (ADR-0011).
	structured bool
	spliced    bool
	sawActive  bool
	idle       bool
	turnEnded  bool
	turnReason turnEndReason
	failure    llmFailure
	usages     map[string]tokenUsage
	answer     bytes.Buffer
	emit       ports.Emit
}

func (p *Provider) execute(parent context.Context, taskID, runID string, input preparedInput, lease *ports.CredentialLease, batchSink ports.ArtifactBatchSink, emit ports.Emit) error {
	ctx, cancel := context.WithTimeout(parent, input.timeout)
	defer cancel()

	runtimeDir, err := os.MkdirTemp("", "workos-deepseek-")
	if err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek Harness runtime workspace is unavailable", true, err)
	}
	defer os.RemoveAll(runtimeDir) //nolint:errcheck -- exact private directory created above
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek Harness runtime workspace is unavailable", true, err)
	}

	command := exec.CommandContext(ctx, p.config.RuntimePath, p.config.runtimeArgs...)
	command.Dir = runtimeDir
	command.Env = p.runtimeEnvironment(runtimeDir, input, lease)
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = shutdownTimeout
	command.Cancel = func() error { return killProcessGroup(command, syscall.SIGKILL) }
	stdin, err := command.StdinPipe()
	if err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek Harness runtime could not be started", true, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek Harness runtime could not be started", true, err)
	}
	if err := command.Start(); err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek Harness runtime could not be started", true, err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = killProcessGroup(command, syscall.SIGKILL)
			_ = command.Wait()
		}
	}()

	reader := newFrameReader(stdout)
	if err := writeRequest(ctx, stdin, 1, "initialize", map[string]any{
		"cwd": runtimeDir, "provider": "deepseek-official", "model": p.config.Model, "maxTokens": input.maxTokens,
	}); err != nil {
		return processError(ctx, err)
	}
	initial, err := awaitResponse(ctx, reader, 1, nil)
	if err != nil {
		return err
	}
	var initialized initializeResult
	if err := json.Unmarshal(initial.Result, &initialized); err != nil || initialized.ServerInfo.Name != "deepseek-harness-sdk-runtime" || initialized.ServerInfo.Version == "" {
		return protocolError("DeepSeek Harness initialization response is incompatible", err)
	}
	if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{
		RunId: runID, ProviderId: ProviderID,
	}}}); err != nil {
		return err
	}

	state := &streamState{runID: runID, sessionID: runID, emit: emit, usages: make(map[string]tokenUsage), structured: input.structured}
	// The single user content block: the versioned task envelope when the
	// task carries pinned context, otherwise the plain goal. Context bytes
	// are always untrusted payload inside the envelope (ADR-0010).
	userText := input.goal
	if input.envelope != "" {
		userText = input.envelope
	}
	if err := writeRequest(ctx, stdin, 2, "session/prompt", map[string]any{
		"sessionId":     runID,
		"contentBlocks": []map[string]string{{"type": "text", "text": userText}},
	}); err != nil {
		return processError(ctx, err)
	}
	promptResponseSeen := false
	for !(promptResponseSeen && state.spliced && state.turnEnded && state.idle) {
		envelope, readErr := reader.next(ctx)
		if readErr != nil {
			return processError(ctx, readErr)
		}
		if len(envelope.ID) != 0 {
			id, idErr := responseID(envelope)
			if idErr != nil || id != 2 || promptResponseSeen {
				return protocolError("DeepSeek Harness returned an unexpected response", idErr)
			}
			if envelope.Error != nil {
				return classifyRPCError(envelope.Error)
			}
			var result promptResult
			if err := json.Unmarshal(envelope.Result, &result); err != nil || result.MessageID == "" {
				return protocolError("DeepSeek Harness prompt response is malformed", err)
			}
			state.messageID = result.MessageID
			state.spliced = state.pendingSplicedID == result.MessageID
			promptResponseSeen = true
			continue
		}
		if err := state.handleNotification(envelope); err != nil {
			return err
		}
	}
	if err := state.finishTurn(); err != nil {
		return err
	}

	if err := writeRequest(ctx, stdin, 3, "shutdown", nil); err != nil {
		return processError(ctx, err)
	}
	if _, err := awaitResponse(ctx, reader, 3, state.handleNotification); err != nil {
		return err
	}
	if err := stdin.Close(); err != nil {
		return ports.NewRunError(ports.ErrorKindTransport, "DeepSeek Harness shutdown failed", true, err)
	}
	if err := command.Wait(); err != nil {
		waited = true
		return processError(ctx, err)
	}
	waited = true

	if input.structured {
		// Strict structured publication (ADR-0011): parse and validate the
		// single JSON review document, atomically materialize every
		// requested output, and only then surface the validated bounded
		// summary and terminal event. Raw JSON never reaches the timeline.
		if batchSink == nil {
			return protocolError("structured review output has no materialization path", nil)
		}
		summary, artifacts, parseErr := parseStructuredOutput(state.answer.String(), input.requestedOutputs)
		if parseErr != nil {
			return parseErr
		}
		outputs := make([]ports.ArtifactOutput, 0, len(artifacts))
		for _, artifact := range artifacts {
			key, title := structuredKeyTitle(artifact.artifactType)
			outputs = append(outputs, ports.ArtifactOutput{
				Key: key, Title: title, Type: artifact.artifactType, Content: []byte(artifact.content),
			})
		}
		if batchErr := batchSink(outputs); batchErr != nil {
			return batchErr
		}
		if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: summary}}}); err != nil {
			return err
		}
	} else if state.answer.Len() != 0 {
		if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: state.answer.String()}}}); err != nil {
			return err
		}
	}
	if len(state.usages) != 0 {
		var inputTokens, outputTokens int64
		for _, usage := range state.usages {
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
		if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_UsageRecorded{UsageRecorded: &agentv1.UsageRecorded{
			InputTokens: inputTokens, OutputTokens: outputTokens, Model: p.config.Model,
		}}}); err != nil {
			return err
		}
	}
	return emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{
		Summary: fmt.Sprintf("Task %s completed by DeepSeek Harness", taskID),
	}}})
}

// runtimeEnvironment builds the allowlisted child environment for exactly
// this one task. The credential lease's secret material enters the child as
// the runtime's API key variable and nowhere else: it never touches the
// harness-host environment, configuration, logs, or any other task's child.
func (p *Provider) runtimeEnvironment(runtimeDir string, input preparedInput, lease *ports.CredentialLease) []string {
	environment := []string{
		"HOME=" + runtimeDir,
		"TMPDIR=" + runtimeDir,
		"LANG=C.UTF-8",
		"DEEPSEEK_API_KEY=" + string(lease.Secret),
		"DEEPSEEK_BASE_URL=" + p.config.BaseURL,
		"DSH_CORDIS_CONFIG=" + p.config.CordisConfigPath,
		"DSH_CWD=" + runtimeDir,
		"DSH_MODEL=" + p.config.Model,
		"DSH_MAX_TOKENS=" + strconv.FormatInt(input.maxTokens, 10),
		"DSH_LLM_STREAM_TIMEOUT_MS=" + strconv.FormatInt(input.timeout.Milliseconds(), 10),
	}
	for _, key := range []string{"PATH", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, p.config.runtimeEnv...)
}

func writeRequest(ctx context.Context, writer io.Writer, id int64, method string, params any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	request, err := marshalRequest(id, method, params)
	if err != nil {
		return err
	}
	_, err = writer.Write(request)
	return err
}

func awaitResponse(ctx context.Context, reader *frameReader, expectedID int64, notification func(rpcEnvelope) error) (rpcEnvelope, error) {
	for {
		envelope, err := reader.next(ctx)
		if err != nil {
			return rpcEnvelope{}, processError(ctx, err)
		}
		if len(envelope.ID) == 0 {
			if notification == nil {
				return rpcEnvelope{}, protocolError("DeepSeek Harness emitted an unexpected notification", nil)
			}
			if err := notification(envelope); err != nil {
				return rpcEnvelope{}, err
			}
			continue
		}
		id, err := responseID(envelope)
		if err != nil || id != expectedID {
			return rpcEnvelope{}, protocolError("DeepSeek Harness returned an unexpected response", err)
		}
		if envelope.Error != nil {
			return rpcEnvelope{}, classifyRPCError(envelope.Error)
		}
		return envelope, nil
	}
}

type frameReader struct {
	scanner *bufio.Scanner
	total   int64
}

func newFrameReader(reader io.Reader) *frameReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximumJSONRPCLineBytes)
	return &frameReader{scanner: scanner}
}

func (r *frameReader) next(ctx context.Context) (rpcEnvelope, error) {
	if !r.scanner.Scan() {
		if ctx.Err() != nil {
			return rpcEnvelope{}, ctx.Err()
		}
		if err := r.scanner.Err(); err != nil {
			return rpcEnvelope{}, errors.New("DeepSeek Harness response exceeded the line limit")
		}
		return rpcEnvelope{}, io.ErrUnexpectedEOF
	}
	r.total += int64(len(r.scanner.Bytes()) + 1)
	if r.total > maximumRuntimeBytes {
		return rpcEnvelope{}, errors.New("DeepSeek Harness response exceeded the run limit")
	}
	return decodeEnvelope(r.scanner.Bytes())
}

func (s *streamState) handleNotification(envelope rpcEnvelope) error {
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

func (s *streamState) handleSessionEvent(eventType string, raw json.RawMessage) error {
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
			if !s.structured {
				if err := s.emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantDelta{AssistantDelta: &agentv1.AssistantDelta{Text: data.Chunk.Text}}}); err != nil {
					return err
				}
			}
			_, _ = s.answer.WriteString(data.Chunk.Text)
		case "usage":
			if err := s.recordUsage(data.Turn, data.Step, data.Chunk.Usage); err != nil {
				return err
			}
		case "reasoning-delta":
			// Reasoning is intentionally neither persisted nor exposed as a capability.
		case "block-start":
			if data.Chunk.BlockType != "text" && data.Chunk.BlockType != "reasoning" {
				return protocolError("DeepSeek Harness attempted an unsupported content block", nil)
			}
		case "block-end":
			var block struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data.Chunk.Block, &block); err != nil || (block.Type != "text" && block.Type != "reasoning") {
				return protocolError("DeepSeek Harness attempted an unsupported content block", err)
			}
		case "finish":
			// turn/end is the durable whole-turn terminal fact.
		case "tool-call-delta":
			return protocolError("DeepSeek Harness attempted an unsupported tool operation", nil)
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
			if block.Type != "text" && block.Type != "reasoning" {
				return protocolError("DeepSeek Harness attempted an unsupported content block", nil)
			}
			if block.Type == "text" {
				if committed.Len()+len(block.Text) > maximumAnswerBytes {
					return protocolError("DeepSeek Harness response exceeded the answer limit", nil)
				}
				_, _ = committed.WriteString(block.Text)
			}
		}
		s.answer = committed
		if len(data.Usage) != 0 && string(data.Usage) != "null" {
			return s.recordUsage(data.Turn, data.Step, data.Usage)
		}
		return nil
	case "agent/inbox/spliced":
		var data struct {
			Inserted []struct {
				ID string `json:"id"`
			} `json:"inserted"`
		}
		if err := json.Unmarshal(raw, &data); err != nil || len(data.Inserted) > 1 {
			return protocolError("DeepSeek Harness inbox event is malformed", err)
		}
		if len(data.Inserted) == 0 {
			return nil
		}
		if data.Inserted[0].ID == "" {
			return protocolError("DeepSeek Harness inbox event is malformed", nil)
		}
		s.pendingSplicedID = data.Inserted[0].ID
		if s.messageID != "" {
			s.spliced = data.Inserted[0].ID == s.messageID
			if !s.spliced {
				return protocolError("DeepSeek Harness spliced an unexpected prompt", nil)
			}
		}
		s.sawActive = true
		return nil
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
	case "turn/start", "step/start", "step/end", "user/message", "request/header", "request/context", "steering/message", "llm/retry", "llm/retry-started", "session/title", "agent-preset/selected":
		return nil
	case "tool/start", "tool/end", "tool/call", "tool/result", "subagent/start", "subagent/end":
		return protocolError("DeepSeek Harness attempted an unsupported tool or subagent operation", nil)
	default:
		return protocolError("DeepSeek Harness emitted unsupported session event "+safeProtocolLabel(eventType), nil)
	}
}

func (s *streamState) recordUsage(turn, step int64, raw json.RawMessage) error {
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

func (s *streamState) finishTurn() error {
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

func classifyRPCError(value *rpcError) error {
	failure := decodeFailure(value.Data)
	if failure.Code == "" && failure.Status == 0 {
		switch value.Code {
		case -32600, -32601, -32602:
			return protocolError("DeepSeek Harness rejected an adapter request", nil)
		default:
			return ports.NewRunError(ports.ErrorKindProvider, "DeepSeek Harness request failed", true, nil)
		}
	}
	return classifyFailure(failure)
}

func classifyFailure(failure llmFailure) error {
	code := strings.ToUpper(strings.TrimSpace(failure.Code))
	switch {
	case failure.Status == 401 || failure.Status == 403 || code == "AUTH" || code == "AUTHENTICATION":
		return ports.NewRunError(ports.ErrorKindAuthentication, "DeepSeek authentication failed", false, nil)
	case failure.Status == 429 || code == "RATE_LIMIT":
		return ports.NewRunError(ports.ErrorKindRateLimit, "DeepSeek rate limit exceeded", true, nil)
	case failure.Status >= 500 && failure.Status <= 599 || code == "SERVER":
		return ports.NewRunError(ports.ErrorKindProvider, "DeepSeek service is temporarily unavailable", true, nil)
	case code == "TIMEOUT":
		return ports.NewRunError(ports.ErrorKindTimeout, "DeepSeek request timed out", true, nil)
	case code == "TRANSPORT" || code == "EMPTY_RESPONSE" || code == "STREAM_CLOSED":
		return ports.NewRunError(ports.ErrorKindTransport, "DeepSeek connection ended unexpectedly", true, nil)
	case code == "INVALID_REQUEST" || code == "CONTEXT_WINDOW_EXCEEDED" || code == "QUOTA":
		return ports.NewRunError(ports.ErrorKindProvider, "DeepSeek rejected the request", false, nil)
	case code == "MALFORMED_RESPONSE":
		return protocolError("DeepSeek returned a malformed response", nil)
	default:
		return ports.NewRunError(ports.ErrorKindProvider, "DeepSeek request failed", false, nil)
	}
}

func protocolError(reason string, cause error) error {
	return ports.NewRunError(ports.ErrorKindProtocol, reason, false, cause)
}

func processError(ctx context.Context, cause error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ports.NewRunError(ports.ErrorKindTimeout, "DeepSeek Harness exceeded its runtime limit", true, context.DeadlineExceeded)
	}
	if errors.Is(cause, io.ErrUnexpectedEOF) {
		return ports.NewRunError(ports.ErrorKindTransport, "DeepSeek Harness ended unexpectedly", true, cause)
	}
	var runErr *ports.RunError
	if errors.As(cause, &runErr) {
		return cause
	}
	if strings.Contains(cause.Error(), "response exceeded") || strings.Contains(cause.Error(), "malformed") || strings.Contains(cause.Error(), "JSON-RPC") {
		return protocolError(cause.Error(), cause)
	}
	return ports.NewRunError(ports.ErrorKindTransport, "DeepSeek Harness communication failed", true, cause)
}

func addTokens(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > math.MaxInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func killProcessGroup(command *exec.Cmd, signal syscall.Signal) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func safeProtocolLabel(value string) string {
	if value == "" || len(value) > 64 {
		return "unrecognized"
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '/' && char != '-' && char != '_' && char != '.' {
			return "unrecognized"
		}
	}
	return value
}
