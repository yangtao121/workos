package genericcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

const (
	maxEventBytes   = 1 << 20
	maxRequestBytes = 1 << 20
	maxOutputBytes  = 4 << 20
	maxEvents       = 1024
)

type Config struct {
	Executable string
	Args       []string
	Timeout    time.Duration
}

type Provider struct{ config Config }

func New(config Config) (*Provider, error) {
	if config.Executable == "" || !filepath.IsAbs(config.Executable) {
		return nil, errors.New("generic CLI executable must be an absolute allowlisted path")
	}
	if config.Timeout <= 0 {
		config.Timeout = 2 * time.Minute
	}
	return &Provider{config: config}, nil
}

func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	info := &harnessv1.HarnessProviderInfo{
		Id: "generic-cli", DisplayName: "Generic CLI Harness", AdapterVersion: "1.0.0",
		Health:       commonv1.HealthState_HEALTH_STATE_HEALTHY,
		Capabilities: &harnessv1.HarnessCapabilities{Streaming: true},
	}
	if _, err := exec.LookPath(p.config.Executable); err != nil {
		info.Health = commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
		info.UnavailableReason = "Generic CLI executable is unavailable"
	}
	return info
}

// Run keeps structured artifact support honestly unsupported (ADR-0008): the
// sink is ignored and requested artifact types are refused outright.
func (p *Provider) Run(ctx context.Context, execution ports.Execution) (runErr error) {
	taskID, input, emit := execution.TaskID, execution.Input, execution.Emit
	if taskID == "" || input == nil || emit == nil {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI execution is invalid", false, nil)
	}
	if len(input.GetOutputArtifactTypes()) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI harness does not support structured artifacts", false, nil)
	}
	if execution.Credential != nil {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI harness does not accept credential leases", false, nil)
	}
	if len(execution.Context) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI harness does not accept resolved context", false, nil)
	}
	inputJSON, err := protojson.Marshal(input)
	if err != nil || len(inputJSON) > maxRequestBytes {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI input exceeds its protocol", false, nil)
	}
	var request bytes.Buffer
	if err := json.NewEncoder(&request).Encode(map[string]any{"version": "workos.harness-cli/v1", "taskId": taskID, "input": json.RawMessage(inputJSON)}); err != nil || request.Len() > maxRequestBytes {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI request exceeds its protocol", false, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	defer func() {
		if runErr != nil && ctx.Err() != nil {
			runErr = fmt.Errorf("generic CLI stopped: %w", ctx.Err())
		}
	}()
	directory, err := os.MkdirTemp("", "workos-cli-")
	if err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "generic CLI workspace is unavailable", true, nil)
	}
	defer os.RemoveAll(directory)
	command := exec.CommandContext(ctx, p.config.Executable, p.config.Args...)
	command.Dir = directory
	command.Env = []string{"HOME=" + directory, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TZ=UTC"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error { return killGroup(command) }
	command.WaitDelay = time.Second
	command.Stdin = &request
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "generic CLI output pipe is unavailable", true, nil)
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return ports.NewRunError(ports.ErrorKindConfiguration, "generic CLI executable is unavailable", false, nil)
	}
	// Close our reader on cancellation too: a descendant that escaped the
	// process group must not keep this RPC waiting on an inherited pipe.
	stopRead := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopRead()
	waited := false
	defer func() {
		_ = killGroup(command)
		_ = stdout.Close()
		if !waited {
			_ = command.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxEventBytes)
	count, total := 0, 0
	var terminal *agentv1.AgentEvent
	for scanner.Scan() {
		total += len(scanner.Bytes()) + 1
		if count >= maxEvents || total > maxOutputBytes {
			return ports.NewRunError(ports.ErrorKindProtocol, "generic CLI output budget exceeded", false, nil)
		}
		var event agentv1.AgentEvent
		if err := protojson.Unmarshal(scanner.Bytes(), &event); err != nil {
			return ports.NewRunError(ports.ErrorKindProtocol, "decode generic CLI event failed", false, nil)
		}
		if err := validateEvent(&event, count, terminal != nil); err != nil {
			return err
		}
		count++
		if isTerminal(&event) {
			terminal = &event
			continue
		}
		if err := emit(&event); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return ports.NewRunError(ports.ErrorKindProtocol, "read generic CLI events failed", false, nil)
	}
	err = command.Wait()
	waited = true
	if err != nil {
		return ports.NewRunError(ports.ErrorKindProvider, "generic CLI execution failed", false, nil)
	}
	if terminal == nil {
		return errors.New("generic CLI ended without a terminal event")
	}
	// A claimed completion is not trusted until the child exits successfully
	// and the complete bounded stream has passed validation.
	return emit(terminal)
}

func validateEvent(event *agentv1.AgentEvent, eventCount int, sawTerminal bool) error {
	if event.Event == nil {
		return errors.New("generic CLI emitted an empty event")
	}
	if event.GetId() != "" || event.GetTaskId() != "" || event.GetSequence() != 0 || event.GetOccurredAt() != nil {
		return errors.New("generic CLI must not set Core-owned event metadata")
	}
	switch event.Event.(type) {
	case *agentv1.AgentEvent_RunStarted:
		if eventCount != 0 {
			return errors.New("generic CLI emitted a duplicate run start")
		}
	case *agentv1.AgentEvent_AssistantDelta, *agentv1.AgentEvent_AssistantMessage,
		*agentv1.AgentEvent_ToolCallStarted, *agentv1.AgentEvent_ToolCallCompleted,
		*agentv1.AgentEvent_RunCompleted, *agentv1.AgentEvent_RunFailed, *agentv1.AgentEvent_RunCancelled:
	default:
		return errors.New("generic CLI emitted an event outside its capabilities")
	}
	if eventCount == 0 {
		started := event.GetRunStarted()
		if started == nil || started.GetRunId() == "" || started.GetProviderId() != "generic-cli" {
			return errors.New("generic CLI first event must start a generic-cli run")
		}
	}
	if sawTerminal {
		return errors.New("generic CLI emitted an event after a terminal event")
	}
	return nil
}

func isTerminal(event *agentv1.AgentEvent) bool {
	return event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil
}

func killGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
