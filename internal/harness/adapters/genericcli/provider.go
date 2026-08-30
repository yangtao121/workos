package genericcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

const maxEventBytes = 1024 * 1024

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
	return &harnessv1.HarnessProviderInfo{
		Id: "generic-cli", DisplayName: "Generic CLI Harness", AdapterVersion: "1.0.0",
		Health:       commonv1.HealthState_HEALTH_STATE_HEALTHY,
		Capabilities: &harnessv1.HarnessCapabilities{Streaming: true},
	}
}

// Run keeps structured artifact support honestly unsupported (ADR-0008): the
// sink is ignored and requested artifact types are refused outright.
func (p *Provider) Run(ctx context.Context, taskID string, input *agentv1.AgentTaskInput, emit ports.Emit, artifacts ports.ArtifactSink) error {
	_ = artifacts
	if len(input.GetOutputArtifactTypes()) != 0 {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI harness does not support structured artifacts", false, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	command := exec.CommandContext(ctx, p.config.Executable, p.config.Args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open generic CLI stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open generic CLI stdout: %w", err)
	}
	var stderr cappedBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start generic CLI: %w", err)
	}
	inputJSON, _ := protojson.Marshal(input)
	envelope := map[string]any{"version": "workos.harness-cli/v1", "taskId": taskID, "input": json.RawMessage(inputJSON)}
	if err := json.NewEncoder(stdin).Encode(envelope); err != nil {
		stop(command)
		return fmt.Errorf("write generic CLI request: %w", err)
	}
	if err := stdin.Close(); err != nil {
		stop(command)
		return fmt.Errorf("close generic CLI stdin: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), maxEventBytes)
	eventCount := 0
	sawTerminal := false
	for scanner.Scan() {
		var event agentv1.AgentEvent
		if err := protojson.Unmarshal(scanner.Bytes(), &event); err != nil {
			stop(command)
			return fmt.Errorf("decode generic CLI event: %w", err)
		}
		if err := validateEvent(&event, eventCount, sawTerminal); err != nil {
			stop(command)
			return err
		}
		eventCount++
		sawTerminal = sawTerminal || isTerminal(&event)
		if err := emit(&event); err != nil {
			stop(command)
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		stop(command)
		return fmt.Errorf("read generic CLI events: %w", err)
	}
	if err := command.Wait(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("generic CLI stopped: %w", ctx.Err())
		}
		return fmt.Errorf("generic CLI failed: %w: %s", err, stderr.String())
	}
	if !sawTerminal {
		return errors.New("generic CLI ended without a terminal event")
	}
	return nil
}

func validateEvent(event *agentv1.AgentEvent, eventCount int, sawTerminal bool) error {
	if event.Event == nil {
		return errors.New("generic CLI emitted an empty event")
	}
	if event.GetId() != "" || event.GetTaskId() != "" || event.GetSequence() != 0 || event.GetOccurredAt() != nil {
		return errors.New("generic CLI must not set Core-owned event metadata")
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

func stop(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	_ = command.Wait()
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(value []byte) (int, error) {
	remaining := 16*1024 - b.Len()
	if remaining <= 0 {
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

var _ io.Writer = (*cappedBuffer)(nil)
