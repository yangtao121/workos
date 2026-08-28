package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

func grantedInstallation(permissions ...string) projectdomain.Installation {
	installation := activeInstallation("notes-app", "1.0.0", manifestDigest)
	installation.GrantedPermissions = permissions
	return installation
}

func grantedTask(id string) agentdomain.Task {
	return agentdomain.Task{ID: id, OwnerUserID: resolveOwner, ProjectID: resolveProject, ProviderID: "fake"}
}

type fakeAppTasks struct {
	submitted  []agentapp.AppSubmitInput
	replayTask *agentdomain.Task
	replayHit  bool
	replayDiff bool
	task       agentdomain.Task
	taskProj   string
	taskErr    error
	events     []agentdomain.Event
}

func (f *fakeAppTasks) SubmitForApp(_ context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	f.submitted = append(f.submitted, input)
	return grantedTask("new-app-task"), nil
}

func (f *fakeAppTasks) GetAppTaskByIdempotency(context.Context, string, string, string) (agentdomain.Task, string, bool, error) {
	if f.replayDiff {
		return agentdomain.Task{}, "sha256:different", true, nil
	}
	if f.replayTask != nil {
		return *f.replayTask, agentdomain.AppTaskRequestDigest("role", "goal"), true, nil
	}
	return agentdomain.Task{}, "", false, nil
}

func (f *fakeAppTasks) GetAppTask(context.Context, string, string, string) (agentdomain.Task, string, error) {
	if f.taskErr != nil {
		return agentdomain.Task{}, "", f.taskErr
	}
	return f.task, f.taskProj, nil
}

func (f *fakeAppTasks) AppTaskEvents(context.Context, string, string, string, int64, int) ([]agentdomain.Event, error) {
	return f.events, nil
}

func newAppAgent(installation projectdomain.Installation, tasks *fakeAppTasks) *AppAgentService {
	service, err := NewAppAgentService(&fakeInstallations{installation: installation}, tasks)
	if err != nil {
		panic(err)
	}
	return service
}

func TestAppAgentRunRequiresExactGrant(t *testing.T) {
	t.Parallel()
	// Granted: run passes and forces the digest and project scope.
	tasks := &fakeAppTasks{}
	service := newAppAgent(grantedInstallation("agent.task.run"), tasks)
	task, err := service.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, "client-key", "role", "goal")
	if err != nil {
		t.Fatalf("granted run denied: %v", err)
	}
	if task.ID != "new-app-task" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if len(tasks.submitted) != 1 {
		t.Fatalf("expected one submission: %d", len(tasks.submitted))
	}
	submission := tasks.submitted[0]
	if submission.ProjectID != resolveProject || submission.AppInstanceID != resolveInstance {
		t.Fatalf("submission not project-scoped: %+v", submission)
	}
	if submission.RequestDigest != agentdomain.AppTaskRequestDigest("role", "goal") {
		t.Fatalf("digest missing from submission: %+v", submission)
	}

	// Empty grant: sanitized denial.
	denied := newAppAgent(grantedInstallation(), &fakeAppTasks{})
	if _, err := denied.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, "k", "", "goal"); !errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("empty grant verdict: %v", err)
	}

	// A stored grant with a foreign capability ID is corruption, not "deny".
	corrupt := newAppAgent(grantedInstallation("totally.unknown"), &fakeAppTasks{})
	if _, err := corrupt.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, "k", "", "goal"); err == nil || errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("corrupt grant verdict must be an internal invariant: %v", err)
	}
}

func TestAppAgentWatchRequiresWatchGrantAndProvenance(t *testing.T) {
	t.Parallel()
	tasks := &fakeAppTasks{task: grantedTask("task-1"), taskProj: resolveProject, events: []agentdomain.Event{{ID: "e1", Sequence: 1}}}
	service := newAppAgent(grantedInstallation("agent.event.watch"), tasks)
	task, events, err := service.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, "task-1", 0, 100)
	if err != nil || task.ID != "task-1" || len(events) != 1 {
		t.Fatalf("granted watch failed: %v %v", events, err)
	}

	// run-only grant cannot watch.
	runOnly := newAppAgent(grantedInstallation("agent.task.run"), tasks)
	if _, _, err := runOnly.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, "task-1", 0, 100); !errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("watch without grant verdict: %v", err)
	}

	// A task whose provenance maps to another project is a sanitized miss.
	foreign := &fakeAppTasks{task: grantedTask("task-1"), taskProj: "0198d7ea-2110-7c42-b659-c5e4d73bc399"}
	mixed := newAppAgent(grantedInstallation("agent.event.watch"), foreign)
	if _, _, err := mixed.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, "task-1", 0, 100); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Fatalf("foreign-project watch verdict: %v", err)
	}

	// Unknown provenance is a sanitized miss too.
	missing := &fakeAppTasks{taskErr: agentdomain.ErrNotFound}
	none := newAppAgent(grantedInstallation("agent.event.watch"), missing)
	if _, _, err := none.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, "task-1", 0, 100); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Fatalf("unknown task watch verdict: %v", err)
	}
}

func TestAppAgentServiceRequiresDependencies(t *testing.T) {
	t.Parallel()
	if _, err := NewAppAgentService(nil, &fakeAppTasks{}); err == nil {
		t.Fatal("nil installations accepted")
	}
	if _, err := NewAppAgentService(&fakeInstallations{}, nil); err == nil {
		t.Fatal("nil tasks accepted")
	}
}

func TestAppTaskSubmitInputCanonicalPayloadProjection(t *testing.T) {
	t.Parallel()
	// The bounded (role, goal) digest is the request identity; nothing else
	// (owner, project, provider) may enter it.
	if agentdomain.AppTaskRequestDigest("r", "g") == agentdomain.AppTaskRequestDigest("r", "other") {
		t.Fatal("goal not covered by digest")
	}
	if !strings.HasPrefix(agentdomain.AppTaskRequestDigest("r", "g"), "sha256:") {
		t.Fatal("digest prefix missing")
	}
}
