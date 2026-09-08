package transport

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/reliability/application"
)

type repairTaskFixture struct {
	agentv1connect.UnimplementedAgentTaskServiceHandler
	task *agentv1.AgentTask
	t    *testing.T
}

func (f *repairTaskFixture) GetTask(_ context.Context, req *connect.Request[agentv1.GetTaskRequest]) (*connect.Response[agentv1.GetTaskResponse], error) {
	if req.Header().Get(identity.UserHeader) != "owner" || req.Header().Get(identity.DeviceHeader) != "device" || req.Msg.GetTaskId() != "task" {
		f.t.Fatal("task read lost identity")
	}
	return connect.NewResponse(&agentv1.GetTaskResponse{Task: f.task}), nil
}
func TestRepairTaskStateRequiresExactProvenance(t *testing.T) {
	f := &repairTaskFixture{t: t}
	_, handler := agentv1connect.NewAgentTaskServiceHandler(f)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := NewRepairSubmitterClient(server.URL, "device")
	row := application.RepairCompletedRow{RepairCandidate: application.RepairCandidate{OwnerUserID: "owner", ProjectID: "project", IncidentID: "incident", AppInstanceID: "installation"}, TaskID: "task"}
	fresh := func() *agentv1.AgentTask {
		return &agentv1.AgentTask{Id: "task", OwnerUserId: "owner", State: agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED, Input: &agentv1.AgentTaskInput{IncidentId: "incident", RepairTarget: &agentv1.RepairTarget{AppInstanceId: "installation"}, TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: "project"}}}}
	}
	for _, tc := range []struct {
		state agentv1.AgentTaskState
		want  application.RepairTaskState
	}{
		{agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED, application.RepairTaskPending},
		{agentv1.AgentTaskState_AGENT_TASK_STATE_RUNNING, application.RepairTaskPending},
		{agentv1.AgentTaskState_AGENT_TASK_STATE_WAITING, application.RepairTaskPending},
		{agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED, application.RepairTaskCompleted},
		{agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED, application.RepairTaskFailed},
		{agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED, application.RepairTaskFailed},
	} {
		f.task = fresh()
		f.task.State = tc.state
		got, err := client.TaskState(context.Background(), row)
		if err != nil || got != tc.want {
			t.Fatalf("state=%v got=%v err=%v", tc.state, got, err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*agentv1.AgentTask)
	}{
		{"owner", func(task *agentv1.AgentTask) { task.OwnerUserId = "other" }},
		{"task", func(task *agentv1.AgentTask) { task.Id = "other" }},
		{"project", func(task *agentv1.AgentTask) {
			task.Input.TargetScope = &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: "other"}}
		}},
		{"incident", func(task *agentv1.AgentTask) { task.Input.IncidentId = "other" }},
		{"installation", func(task *agentv1.AgentTask) { task.Input.RepairTarget.AppInstanceId = "other" }},
		{"missing target", func(task *agentv1.AgentTask) { task.Input.RepairTarget = nil }},
		{"missing input", func(task *agentv1.AgentTask) { task.Input = nil }},
		{"unknown state", func(task *agentv1.AgentTask) { task.State = agentv1.AgentTaskState_AGENT_TASK_STATE_UNSPECIFIED }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.task = fresh()
			tc.mutate(f.task)
			if _, err := client.TaskState(context.Background(), row); connect.CodeOf(err) != connect.CodeInternal {
				t.Fatalf("untrusted task accepted: %v", err)
			}
		})
	}
	f.task = nil
	if _, err := client.TaskState(context.Background(), row); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("missing task accepted: %v", err)
	}
}
