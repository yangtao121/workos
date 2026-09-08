package genericcli

import (
	"context"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"google.golang.org/protobuf/proto"
)

func repairExecution() ports.Execution {
	target := &agentv1.RepairTarget{AppInstanceId: "installation", AppId: "fixture", Version: "1.0.0", ManifestDigest: "sha256:fixture", ProjectRevision: 2}
	return ports.Execution{TaskID: "task-1", Input: &agentv1.AgentTaskInput{IncidentId: "incident", RepairTarget: target}, Repair: &executionv1.RepairBuildInput{TaskId: "task-1", Target: proto.Clone(target).(*agentv1.RepairTarget), Source: &appv1.AppSourceBundle{Id: "base-source", Digest: "sha256:source", Files: []*appv1.AppSourceFile{{Path: "main.go", Content: []byte("pinned source")}}}, BaseImage: "toolchain@sha256:fixture", BuildCommand: []string{"go", "build"}, TestCommand: []string{"go", "test"}}, Emit: func(*agentv1.AgentEvent) error { return nil }, RepairSource: func([]*appv1.AppSourceFile) error { return nil }}
}
func TestRepairCandidatePublishesOnlyAfterSuccessfulExit(t *testing.T) {
	for _, mode := range []string{"repair-valid", "repair-exit-failure", "repair-failed", "repair-missing", "repair-duplicate", "repair-after-terminal", "repair-before-start", "repair-oversize", "repair-unsolicited"} {
		t.Run(mode, func(t *testing.T) {
			execution := repairExecution()
			published, completed := 0, false
			if mode == "repair-unsolicited" {
				execution.Repair = nil
				execution.Input.RepairTarget = nil
			}
			execution.RepairSource = func(files []*appv1.AppSourceFile) error {
				if len(files) != 1 || string(files[0].Content) != "candidate" {
					t.Error("wrong files")
				}
				published++
				return nil
			}
			execution.Emit = func(event *agentv1.AgentEvent) error {
				if event.GetRunCompleted() != nil {
					completed = true
					if published != 1 {
						t.Error("completion preceded candidate")
					}
				}
				return nil
			}
			err := helperProvider(t, mode, time.Duration(helperTimeoutScale)*time.Second).Run(context.Background(), execution)
			if mode == "repair-valid" {
				if err != nil || !completed || published != 1 {
					t.Fatalf("valid repair: %v published=%d completed=%v", err, published, completed)
				}
			} else if published != 0 || completed || (err == nil && mode != "repair-failed") {
				t.Fatalf("invalid repair: %v published=%d completed=%v", err, published, completed)
			}
		})
	}
}
func TestRepairCandidateRejectsSinkFailureAndInputDrift(t *testing.T) {
	execution := repairExecution()
	failure := errors.New("candidate sink failed")
	completed := false
	execution.RepairSource = func([]*appv1.AppSourceFile) error { return failure }
	execution.Emit = func(event *agentv1.AgentEvent) error {
		completed = completed || event.GetRunCompleted() != nil
		return nil
	}
	if err := helperProvider(t, "repair-valid", time.Duration(helperTimeoutScale)*time.Second).Run(context.Background(), execution); !errors.Is(err, failure) || completed {
		t.Fatalf("sink failure: %v completed=%v", err, completed)
	}
	for _, change := range []func(*ports.Execution){
		func(e *ports.Execution) { e.Repair.TaskId = "different" },
		func(e *ports.Execution) { e.Repair.Target.Version = "2.0.0" },
		func(e *ports.Execution) { e.Repair = nil },
		func(e *ports.Execution) { e.RepairSource = nil },
		func(e *ports.Execution) { e.Input.OutputArtifactTypes = []string{"document.markdown.v1"} },
	} {
		e := repairExecution()
		change(&e)
		if _, _, _, err := prepareRequest(e, time.Second); err == nil {
			t.Fatal("unbound or mixed repair reached child")
		}
	}
}
