// Command restart is an acceptance helper used by make test-integration to
// prove that completed task state and event cursors survive process restarts.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: restart seed | restart verify TASK_ID")
	}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := waitReady(ctx, client, baseURL); err != nil {
		return err
	}
	switch os.Args[1] {
	case "seed":
		return seed(ctx, client, baseURL)
	case "verify":
		if len(os.Args) != 3 {
			return errors.New("verify requires a task id")
		}
		return verify(ctx, client, baseURL, os.Args[2])
	default:
		return errors.New("usage: restart seed | restart verify TASK_ID")
	}
}

func seed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	key := fmt.Sprintf("restart-project-%d", time.Now().UnixNano())
	providerID := os.Getenv("WORKOS_TEST_PROVIDER")
	if providerID == "" {
		providerID = "fake"
	}
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: key, Name: "Restart Persistence"}))
	if err != nil {
		return fmt.Errorf("create restart project: %w", err)
	}
	activeProject := created.Msg.GetProject()
	if providerID != "fake" {
		bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
			ProjectId: activeProject.GetId(), ExpectedRevision: activeProject.GetRevision(),
			Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: providerID},
		}))
		if err != nil {
			return fmt.Errorf("bind restart project: %w", err)
		}
		activeProject = bound.Msg.GetProject()
		if activeProject.GetHarnessBinding().GetCredentialRef() != "" {
			return errors.New("server binding unexpectedly exposed a credential reference")
		}
	}
	response, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: activeProject.GetId()}},
			Role:        "general", Goal: "persist this completed run across service restart",
		},
	}))
	if err != nil {
		return fmt.Errorf("submit restart task: %w", err)
	}
	taskID := response.Msg.GetTask().GetId()
	if response.Msg.GetTask().GetProviderId() != providerID {
		return fmt.Errorf("task provider snapshot mismatch: got %q want %q", response.Msg.GetTask().GetProviderId(), providerID)
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID}))
	if err != nil {
		return fmt.Errorf("watch restart task: %w", err)
	}
	for stream.Receive() {
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("complete restart task: %w", err)
	}
	fmt.Println(taskID)
	return nil
}

func verify(ctx context.Context, client *http.Client, baseURL, taskID string) error {
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	response, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		return fmt.Errorf("get task after restart: %w", err)
	}
	task := response.Msg.GetTask()
	expectedProvider := os.Getenv("WORKOS_TEST_PROVIDER")
	if expectedProvider == "" {
		expectedProvider = "fake"
	}
	if task.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || task.GetLastEventSequence() < 2 || task.GetProviderId() != expectedProvider {
		return fmt.Errorf("task was not durably completed: state=%s sequence=%d", task.GetState(), task.GetLastEventSequence())
	}
	projectID := task.GetInput().GetTargetScope().GetProjectId()
	projectResponse, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("get bound project after restart: %w", err)
	}
	if expectedProvider == "fake" {
		if projectResponse.Msg.GetProject().GetHarnessBinding() != nil {
			return errors.New("global-default Project unexpectedly gained a persisted binding")
		}
	} else if binding := projectResponse.Msg.GetProject().GetHarnessBinding(); binding.GetProviderId() != expectedProvider || binding.GetCredentialRef() != "" {
		return fmt.Errorf("Project binding was not durably restored: provider=%q", binding.GetProviderId())
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{
		TaskId: taskID,
	}))
	if err != nil {
		return fmt.Errorf("resume events after restart: %w", err)
	}
	count := 0
	startedProvider := ""
	for stream.Receive() {
		count++
		if started := stream.Msg().GetEvent().GetRunStarted(); started != nil {
			startedProvider = started.GetProviderId()
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("read resumed event after restart: %w", err)
	}
	if int64(count) != task.GetLastEventSequence() || startedProvider != expectedProvider {
		return fmt.Errorf("unexpected restored event stream: count=%d provider=%q", count, startedProvider)
	}
	fmt.Printf("restart persistence verified for task %s\n", taskID)
	return nil
}

func waitReady(ctx context.Context, client *http.Client, baseURL string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gateway did not become ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
