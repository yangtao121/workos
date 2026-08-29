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

// currentGrantRevision is the session-derived epoch fixtures pass; it matches
// the activeInstallation default of 1.
const currentGrantRevision = 1

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
	task, err := service.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "client-key", "role", "goal")
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
	if _, err := denied.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "k", "", "goal"); !errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("empty grant verdict: %v", err)
	}

	// A stored grant with a foreign capability ID is corruption, not "deny".
	corrupt := newAppAgent(grantedInstallation("totally.unknown"), &fakeAppTasks{})
	if _, err := corrupt.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "k", "", "goal"); err == nil || errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("corrupt grant verdict must be an internal invariant: %v", err)
	}
}

// TestAppAgentValidatesEntireGrantBeforeMembership pins the full-snapshot
// validation: a valid capability followed by trailing corruption must fail
// closed as an internal invariant, never authorize.
func TestAppAgentValidatesEntireGrantBeforeMembership(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"unknown after the requested capability":  {"agent.task.run", "totally.unknown"},
		"unknown before the requested capability": {"totally.unknown", "agent.task.run"},
		"duplicate entries":                       {"agent.task.run", "agent.task.run"},
		"unsorted entries":                        {"agent.task.run", "agent.event.watch"},
	}
	for name, grant := range cases {
		service := newAppAgent(grantedInstallation(grant...), &fakeAppTasks{})
		if _, err := service.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "k", "", "goal"); err == nil || errors.Is(err, ErrAppNotGranted) {
			t.Errorf("%s: verdict must be an internal invariant, got %v", name, err)
		}
	}
	// The canonical sorted run+watch grant keeps both methods working, and a
	// single-capability grant keeps the separation exact.
	both := newAppAgent(grantedInstallation("agent.event.watch", "agent.task.run"), &fakeAppTasks{})
	if _, err := both.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "k", "", "goal"); err != nil {
		t.Errorf("canonical grant rejected: %v", err)
	}
	if _, _, err := both.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Errorf("watch on canonical grant should pass authorization (failure is provenance): %v", err)
	}
	runOnly := newAppAgent(grantedInstallation("agent.task.run"), &fakeAppTasks{})
	if _, _, err := runOnly.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100); !errors.Is(err, ErrAppNotGranted) {
		t.Errorf("watch with run-only grant verdict: %v", err)
	}
}

func TestAppAgentWatchRequiresWatchGrantAndProvenance(t *testing.T) {
	t.Parallel()
	tasks := &fakeAppTasks{task: grantedTask("task-1"), taskProj: resolveProject, events: []agentdomain.Event{{ID: "e1", Sequence: 1}}}
	service := newAppAgent(grantedInstallation("agent.event.watch"), tasks)
	task, events, err := service.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100)
	if err != nil || task.ID != "task-1" || len(events) != 1 {
		t.Fatalf("granted watch failed: %v %v", events, err)
	}

	// run-only grant cannot watch.
	runOnly := newAppAgent(grantedInstallation("agent.task.run"), tasks)
	if _, _, err := runOnly.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100); !errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("watch without grant verdict: %v", err)
	}

	// A task whose provenance maps to another project is a sanitized miss.
	foreign := &fakeAppTasks{task: grantedTask("task-1"), taskProj: "0198d7ea-2110-7c42-b659-c5e4d73bc399"}
	mixed := newAppAgent(grantedInstallation("agent.event.watch"), foreign)
	if _, _, err := mixed.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100); !errors.Is(err, agentdomain.ErrNotFound) {
		t.Fatalf("foreign-project watch verdict: %v", err)
	}

	// Unknown provenance is a sanitized miss too.
	missing := &fakeAppTasks{taskErr: agentdomain.ErrNotFound}
	none := newAppAgent(grantedInstallation("agent.event.watch"), missing)
	if _, _, err := none.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, currentGrantRevision, "task-1", 0, 100); !errors.Is(err, agentdomain.ErrNotFound) {
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

// TestAppAgentRequiresExactGrantRevision pins the ADR-0003 re-authorization:
// the session-derived epoch must equal the re-resolved active installation's
// epoch exactly — older, newer, and absent revisions are the same stale
// verdict, and no submission happens.
func TestAppAgentRequiresExactGrantRevision(t *testing.T) {
	t.Parallel()
	// The exact epoch still authorizes a granted capability.
	tasks := &fakeAppTasks{}
	service := newAppAgent(grantedInstallation("agent.task.run"), tasks)
	if _, err := service.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, 1, "k", "", "goal"); err != nil {
		t.Fatalf("exact epoch run denied: %v", err)
	}
	if len(tasks.submitted) != 1 {
		t.Fatalf("exact epoch must submit exactly once, got %d", len(tasks.submitted))
	}
	// A stale epoch fails even though the capability is still granted — the
	// whole old session is invalid, not just removed capabilities.
	epochBumped := grantedInstallation("agent.task.run")
	epochBumped.GrantRevision = 2
	bumped := newAppAgent(epochBumped, &fakeAppTasks{})
	if _, err := bumped.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, 1, "k", "", "goal"); !errors.Is(err, ErrAppGrantStale) {
		t.Fatalf("old session epoch must be stale, got %v", err)
	}
	// A session claiming a future epoch is equally invalid: exact equality.
	if _, err := bumped.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, 3, "k", "", "goal"); !errors.Is(err, ErrAppGrantStale) {
		t.Fatalf("future session epoch must be stale, got %v", err)
	}
	// Absent and negative revisions are indistinguishable from a mismatch.
	for _, revision := range []int64{0, -1} {
		if _, err := bumped.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, revision, "k", "", "goal"); !errors.Is(err, ErrAppGrantStale) {
			t.Fatalf("revision %d must be the stale verdict, got %v", revision, err)
		}
	}
}

// TestAppAgentRevisionPrecedesGrantMembership pins the ordering: the epoch
// comparison happens before the grant-membership check, so a stale session
// never learns whether its capability is still granted.
func TestAppAgentRevisionPrecedesGrantMembership(t *testing.T) {
	t.Parallel()
	// Capability no longer granted AND epoch matches: the plain grant denial.
	revoked := newAppAgent(grantedInstallation("agent.event.watch"), &fakeAppTasks{})
	if _, err := revoked.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, 1, "k", "", "goal"); !errors.Is(err, ErrAppNotGranted) {
		t.Fatalf("epoch-matching grant denial verdict: %v", err)
	}
	// Capability still granted but epoch moved: the stale verdict wins.
	kept := grantedInstallation("agent.task.run")
	kept.GrantRevision = 5
	moved := newAppAgent(kept, &fakeAppTasks{})
	if _, err := moved.RunAgentTask(context.Background(), resolveOwner, resolveProject, resolveInstance, 4, "k", "", "goal"); !errors.Is(err, ErrAppGrantStale) {
		t.Fatalf("stale epoch must win over membership, got %v", err)
	}
}

// TestAppAgentWatchRevisionRejectsAndTerminates pins the watch-side
// authorization: a stale epoch is a PermissionDenied-class denial on the
// very first polling round, so the stream ends without forwarding events.
func TestAppAgentWatchRevisionRejectsAndTerminates(t *testing.T) {
	t.Parallel()
	tasks := &fakeAppTasks{task: grantedTask("task-1"), taskProj: resolveProject, events: []agentdomain.Event{{ID: "e1", Sequence: 1}}}
	// Matching epoch: authorization passes and the events flow.
	service := newAppAgent(grantedInstallation("agent.event.watch"), tasks)
	_, events, err := service.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, 1, "task-1", 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatalf("matching epoch watch must authorize and read events, got %v %v", events, err)
	}
	// Stale epoch: denied before any task/event read.
	staleInstallation := grantedInstallation("agent.event.watch")
	staleInstallation.GrantRevision = 2
	stale := newAppAgent(staleInstallation, tasks)
	if _, _, err := stale.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, 1, "task-1", 0, 100); !errors.Is(err, ErrAppGrantStale) {
		t.Fatalf("stale epoch watch verdict: %v", err)
	}
	if _, _, err := stale.WatchAgentTaskEvents(context.Background(), resolveOwner, resolveProject, resolveInstance, 0, "task-1", 0, 100); !errors.Is(err, ErrAppGrantStale) {
		t.Fatalf("absent epoch watch verdict: %v", err)
	}
}
