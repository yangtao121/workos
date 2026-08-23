//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

func TestProjectToHarnessVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	projects := projectv1connect.NewProjectServiceClient(httpClient, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(httpClient, baseURL)

	key := fmt.Sprintf("integration-project-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Foundation Integration", Icon: "◈",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	if project.GetId() == "" || project.GetRevision() != 1 {
		t.Fatalf("unexpected project: %#v", project)
	}

	repeated, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "ignored duplicate name",
	}))
	if err != nil {
		t.Fatalf("repeat idempotent create: %v", err)
	}
	if repeated.Msg.GetProject().GetId() != project.GetId() {
		t.Fatal("idempotent create returned a different project")
	}

	updatedName := "Foundation Verified"
	updated, err := projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(), Name: &updatedName,
	}))
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if updated.Msg.GetProject().GetRevision() != 2 {
		t.Fatalf("expected revision 2, got %d", updated.Msg.GetProject().GetRevision())
	}
	_, err = projects.UpdateProject(ctx, connect.NewRequest(&projectv1.UpdateProjectRequest{
		ProjectId: project.GetId(), ExpectedRevision: 1, Name: &updatedName,
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	taskKey := fmt.Sprintf("integration-task-%d", time.Now().UnixNano())
	submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: taskKey,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: project.GetId()}},
			Role:        "general", Goal: "verify durable fake harness events",
		},
	}))
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	taskID := submitted.Msg.GetTask().GetId()
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID}))
	if err != nil {
		t.Fatalf("watch task: %v", err)
	}
	var sequence int64
	terminalEvents := 0
	for stream.Receive() {
		event := stream.Msg().GetEvent()
		if event.GetSequence() != sequence+1 {
			t.Fatalf("event sequence jumped from %d to %d", sequence, event.GetSequence())
		}
		sequence = event.GetSequence()
		if event.GetRunCompleted() != nil || event.GetRunFailed() != nil || event.GetRunCancelled() != nil {
			terminalEvents++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("task stream failed: %v", err)
	}
	if sequence < 3 || terminalEvents != 1 {
		t.Fatalf("expected ordered stream with one terminal event, sequence=%d terminal=%d", sequence, terminalEvents)
	}
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		t.Fatalf("get final task: %v", err)
	}
	if final.Msg.GetTask().GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("expected completed task, got %s", final.Msg.GetTask().GetState())
	}

	resumed, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{
		TaskId: taskID, AfterSequence: sequence - 1,
	}))
	if err != nil {
		t.Fatalf("resume task stream: %v", err)
	}
	resumedCount := 0
	for resumed.Receive() {
		resumedCount++
	}
	if err := resumed.Err(); err != nil {
		t.Fatalf("resumed stream failed: %v", err)
	}
	if resumedCount != 1 {
		t.Fatalf("expected one resumed event, got %d", resumedCount)
	}
}
