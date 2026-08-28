package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type fakeAppAgentService struct {
	task        agentdomain.Task
	err         error
	events      []agentdomain.Event
	watchTask   agentdomain.Task
	calls       int
	lastRunArgs [6]string
}

func (f *fakeAppAgentService) RunAgentTask(_ context.Context, owner, project, instance, key, role, goal string) (agentdomain.Task, error) {
	f.calls++
	f.lastRunArgs = [6]string{owner, project, instance, key, role, goal}
	if f.err != nil {
		return agentdomain.Task{}, f.err
	}
	return f.task, nil
}

func (f *fakeAppAgentService) WatchAgentTaskEvents(_ context.Context, _, _, _, _ string, _ int64, _ int) (agentdomain.Task, []agentdomain.Event, error) {
	if f.err != nil {
		return agentdomain.Task{}, nil, f.err
	}
	return f.watchTask, f.events, nil
}

func appAgentContext(ctx context.Context) context.Context {
	return identity.WithContext(ctx, identity.Identity{UserID: "owner-1", DeviceID: "device-1"})
}

func helperRunRequest(project, instance, key, role, goal string) *connect.Request[agentv1.RunAgentTaskRequest] {
	return connect.NewRequest(&agentv1.RunAgentTaskRequest{
		ProjectId: project, AppInstanceId: instance,
		ClientIdempotencyKey: key, Role: role, Goal: goal,
	})
}

func assertConnectCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", code)
	}
	connectErr := &connect.Error{}
	if !errors.As(err, &connectErr) || connectErr.Code().String() != code {
		t.Fatalf("expected %s error, got %v", code, err)
	}
}

func TestAppAgentHandlerRunMapsCanonicalResponse(t *testing.T) {
	service := &fakeAppAgentService{task: agentdomain.Task{ID: "task-1", State: agentdomain.StateQueued, LastEventSequence: 4}}
	handler := NewAppAgent(service)
	response, err := handler.RunAgentTask(appAgentContext(context.Background()), helperRunRequest("proj", "inst", "key", "role", "goal"))
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if response.Msg.GetTaskId() != "task-1" || response.Msg.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_QUEUED || response.Msg.GetLastEventSequence() != 4 {
		t.Fatalf("unexpected response: %+v", response.Msg)
	}
	if service.lastRunArgs[0] != "owner-1" {
		t.Fatal("owner must come from the identity context only")
	}
}

func TestAppAgentHandlerWatchStreamsAndTerminates(t *testing.T) {
	service := &fakeAppAgentService{
		watchTask: agentdomain.Task{ID: "task-1", State: agentdomain.StateCompleted, LastEventSequence: 1},
		events:    []agentdomain.Event{{ID: "e1", Sequence: 1, Payload: []byte(`{"runCompleted":{"summary":"done"}}`)}},
	}
	// Drive the real Connect handler over HTTP so the streaming path is the
	// production one end to end.
	path, handler := agentv1connect.NewAppAgentServiceHandler(NewAppAgent(service))
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := agentv1connect.NewAppAgentServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&agentv1.WatchAgentTaskEventsRequest{
		ProjectId: "proj", AppInstanceId: "inst", TaskId: "task-1", AfterSequence: 0,
	})
	request.Header().Set(identity.UserHeader, "owner-1")
	request.Header().Set(identity.DeviceHeader, "device-1")
	stream, err := client.WatchAgentTaskEvents(context.Background(), request)
	if err != nil {
		t.Fatalf("watch failed: %v", err)
	}
	var summaries []string
	for stream.Receive() {
		if completed := stream.Msg().GetEvent().GetRunCompleted(); completed != nil {
			summaries = append(summaries, completed.GetSummary())
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(summaries) != 1 || summaries[0] != "done" {
		t.Fatalf("unexpected stream: %v", summaries)
	}
}

func TestAppAgentHandlerFailsClosedWithoutIdentity(t *testing.T) {
	handler := NewAppAgent(&fakeAppAgentService{})
	_, err := handler.RunAgentTask(context.Background(), helperRunRequest("p", "i", "k", "", "g"))
	assertConnectCode(t, err, "unauthenticated")
}

func TestAppAgentHandlerErrorMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"invalid", agentdomain.ErrInvalid, "invalid_argument"},
		{"project invalid", projectdomain.ErrInvalid, "invalid_argument"},
		{"not found", agentdomain.ErrNotFound, "not_found"},
		{"project denied", agentdomain.ErrProjectDenied, "permission_denied"},
		{"not granted", orchestration.ErrAppNotGranted, "permission_denied"},
		{"conflict", agentdomain.ErrIdempotencyConflict, "aborted"},
		{"store unavailable", projectports.ErrStoreUnavailable, "unavailable"},
		{"unknown", errors.New("pgx something"), "internal"},
	}
	for _, testCase := range cases {
		service := &fakeAppAgentService{err: testCase.err}
		_, err := NewAppAgent(service).RunAgentTask(appAgentContext(context.Background()), helperRunRequest("p", "i", "k", "", "g"))
		assertConnectCode(t, err, testCase.want)
	}
}

func TestAppAgentHandlerSanitizedMessages(t *testing.T) {
	service := &fakeAppAgentService{err: errors.New("database constraint violation on agent_app_task_requests")}
	_, err := NewAppAgent(service).RunAgentTask(appAgentContext(context.Background()), helperRunRequest("p", "i", "k", "", "g"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "constraint") {
		t.Fatalf("internal detail leaked: %v", err)
	}
}
