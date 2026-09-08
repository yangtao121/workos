package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/harness/adapters/fake"
	"github.com/yangtao121/workos/internal/harness/broker"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type repairCore struct {
	input         *executionv1.RepairBuildInput
	mode          string
	reads, writes int
}

func (c *repairCore) ResolveRepairBuildInput(_ context.Context, r *connect.Request[executionv1.ResolveRepairBuildInputRequest]) (*connect.Response[executionv1.ResolveRepairBuildInputResponse], error) {
	c.reads++
	if r.Msg.GetWorkerId() != "repair-worker" || r.Msg.GetLeaseId() != "repair-lease" {
		return nil, errors.New("wrong lease")
	}
	if c.mode == "resolve failure" {
		return nil, errors.New("unavailable")
	}
	return connect.NewResponse(&executionv1.ResolveRepairBuildInputResponse{Input: c.input}), nil
}
func (c *repairCore) SubmitRepairSourceCandidate(_ context.Context, r *connect.Request[executionv1.SubmitRepairSourceCandidateRequest]) (*connect.Response[executionv1.SubmitRepairSourceCandidateResponse], error) {
	c.writes++
	if c.mode == "write failure" {
		return nil, errors.New("lease lost")
	}
	taskID := c.input.GetTaskId()
	if c.mode == "wrong receipt" {
		taskID = "other-task"
	}
	return connect.NewResponse(&executionv1.SubmitRepairSourceCandidateResponse{Candidate: &executionv1.RepairSourceCandidate{TaskId: taskID, SourceBundleId: "candidate", SourceDigest: "sha256:candidate", CreatedAt: timestamppb.New(time.Now().UTC())}}), nil
}

type repairWorkerProvider struct {
	ports.Provider
	mode string
	runs int
}

func (p *repairWorkerProvider) Describe() *harnessv1.HarnessProviderInfo {
	info := p.Provider.Describe()
	if p.mode == "unsupported" {
		info.Capabilities.RepairSourceCandidates = false
	}
	return info
}
func (p *repairWorkerProvider) Run(ctx context.Context, e ports.Execution) error {
	p.runs++
	if p.mode == "skip candidate" {
		if err := e.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{RunId: "run", ProviderId: "fake"}}}); err != nil {
			return err
		}
		return e.Emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{}}})
	}
	return p.Provider.Run(ctx, e)
}
func TestWorkerRequiresBoundRepairInputAndSuccessfulCandidateReceipt(t *testing.T) {
	for _, mode := range []string{"valid", "resolve failure", "input drift", "unsupported", "skip candidate", "write failure", "wrong receipt"} {
		t.Run(mode, func(t *testing.T) {
			target := &agentv1.RepairTarget{AppId: "fixture", Version: "1.0.0"}
			core := &fakeCore{lease: &executionv1.TaskLease{LeaseId: "repair-lease", WorkerId: "repair-worker", Task: &agentv1.AgentTask{Id: "repair-task", ProviderId: "fake", Input: &agentv1.AgentTaskInput{IncidentId: "incident", RepairTarget: target}}}}
			repairs := &repairCore{mode: mode, input: &executionv1.RepairBuildInput{TaskId: "repair-task", Target: proto.Clone(target).(*agentv1.RepairTarget), Source: &appv1.AppSourceBundle{Id: "source", Digest: "sha256:source", Files: []*appv1.AppSourceFile{{Path: "main.go", Content: []byte("package main")}}}, BaseImage: "base", BuildCommand: []string{"build"}, TestCommand: []string{"test"}}}
			if mode == "input drift" {
				repairs.input.Target.Version = "2.0.0"
			}
			provider := &repairWorkerProvider{Provider: fake.New(ids.UUIDv7{}), mode: mode}
			value := broker.New(provider)
			server := newWorkerTestServer(t, core)
			worker := New("repair-worker", server.URL, time.Millisecond, value, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, server.Client())
			worker.repairs = repairs
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			worker.process(ctx, core.lease)
			completed, failed := 0, 0
			for _, event := range core.appended {
				if event.GetRunCompleted() != nil {
					completed++
				}
				if event.GetRunFailed() != nil {
					failed++
				}
			}
			if repairs.reads != 1 {
				t.Fatalf("resolved %d times", repairs.reads)
			}
			if mode == "valid" {
				if completed != 1 || failed != 0 || repairs.writes != 1 {
					t.Fatalf("valid completion=%d failed=%d writes=%d", completed, failed, repairs.writes)
				}
			} else if completed != 0 || failed != 1 {
				t.Fatalf("bad completion=%d failed=%d", completed, failed)
			}
			if (mode == "resolve failure" || mode == "input drift" || mode == "unsupported") && (provider.runs != 0 || repairs.writes != 0) {
				t.Fatal("refused input started provider")
			}
		})
	}
}
