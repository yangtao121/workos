package genericcli

import (
	"bufio"
	"bytes"
	"context"
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
		Id: "generic-cli", DisplayName: "Generic CLI Harness", AdapterVersion: "2.0.0",
		Health:       commonv1.HealthState_HEALTH_STATE_HEALTHY,
		Capabilities: &harnessv1.HarnessCapabilities{Streaming: true, StructuredArtifacts: true, SupportedArtifactTypes: []string{"document.markdown.v1", "code.unified-diff.v1"}, SupportedContextRefTypes: []string{"artifact.review.v1"}, HardRuntimeDeadline: p.config.Timeout >= time.Second, MaxRuntimeSeconds: int64(p.config.Timeout / time.Second)},
	}
	if _, err := exec.LookPath(p.config.Executable); err != nil {
		info.Health = commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
		info.UnavailableReason = "Generic CLI executable is unavailable"
	}
	return info
}

// Run publishes requested artifacts only after a complete successful CLI run.
func (p *Provider) Run(ctx context.Context, execution ports.Execution) (runErr error) {
	emit := execution.Emit
	request, timeout, requested, err := prepareRequest(execution, p.config.Timeout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	command.Stdin = bytes.NewReader(request)
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
	var artifacts []ports.ArtifactOutput
	seen := make(map[string]bool, len(requested))
	for scanner.Scan() {
		total += len(scanner.Bytes()) + 1
		if count >= maxEvents || total > maxOutputBytes {
			return ports.NewRunError(ports.ErrorKindProtocol, "generic CLI output budget exceeded", false, nil)
		}
		var response harnessv1.HarnessCLIResponse
		if err := protojson.Unmarshal(scanner.Bytes(), &response); err != nil {
			return ports.NewRunError(ports.ErrorKindProtocol, "decode generic CLI event failed", false, nil)
		}
		if terminal != nil {
			return errors.New("generic CLI emitted an event after a terminal event")
		}
		if artifact := response.GetArtifact(); artifact != nil {
			if count == 0 {
				return errors.New("generic CLI artifact preceded run start")
			}
			output, err := decodeArtifact(artifact, requested, seen)
			if err != nil {
				return err
			}
			for _, previous := range artifacts {
				if previous.Key == output.Key {
					return errors.New("generic CLI artifact key is duplicated")
				}
			}
			artifacts = append(artifacts, output)
			count++
			continue
		}
		event := response.GetEvent()
		if event == nil {
			return errors.New("generic CLI response has no payload")
		}
		if err := validateEvent(event, count); err != nil {
			return err
		}
		count++
		if isTerminal(event) {
			terminal = event
			continue
		}
		if err := emit(event); err != nil {
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
	if err := ctx.Err(); err != nil {
		return err
	}
	// Failed/cancelled runs never publish their buffered artifacts.
	if terminal.GetRunCompleted() != nil {
		if len(artifacts) != len(requested) {
			return errors.New("generic CLI completed without every requested artifact")
		}
		if len(artifacts) > 0 {
			if execution.ArtifactsBatch != nil {
				err = execution.ArtifactsBatch(artifacts)
			} else {
				err = execution.Artifacts(artifacts[0])
			}
			if err != nil {
				return err
			}
		}
	}
	return emit(terminal)
}

func validateEvent(event *agentv1.AgentEvent, eventCount int) error {
	if eventCount == 0 && event.GetRunStarted() == nil {
		return errors.New("generic CLI first event must start the run")
	}
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
