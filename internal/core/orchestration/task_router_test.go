package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type fakeAgents struct {
	existing               *agentdomain.Task
	submitted              []agentapp.SubmitInput
	appSubmitted           []agentapp.AppSubmitInput
	appReplayTask          *agentdomain.Task
	appReplayDigestDiffers bool
}

func (f *fakeAgents) GetByIdempotency(context.Context, string, string) (agentdomain.Task, error) {
	if f.existing == nil {
		return agentdomain.Task{}, agentdomain.ErrNotFound
	}
	return *f.existing, nil
}

func (f *fakeAgents) Submit(_ context.Context, input agentapp.SubmitInput) (agentdomain.Task, error) {
	f.submitted = append(f.submitted, input)
	return agentdomain.Task{ID: "new-task", ProviderID: input.ProviderID}, nil
}

func (f *fakeAgents) SubmitForApp(_ context.Context, input agentapp.AppSubmitInput) (agentdomain.Task, error) {
	f.appSubmitted = append(f.appSubmitted, input)
	return agentdomain.Task{ID: "new-app-task", ProviderID: input.ProviderID}, nil
}

func (f *fakeAgents) GetAppTaskByIdempotency(context.Context, string, string, string) (agentdomain.Task, string, bool, error) {
	if f.appReplayDigestDiffers {
		return agentdomain.Task{}, "sha256:different", true, nil
	}
	if f.appReplayTask != nil {
		return *f.appReplayTask, agentdomain.AppTaskRequestDigest("role", "goal"), true, nil
	}
	return agentdomain.Task{}, "", false, nil
}

func (f *fakeAgents) GetAppTask(context.Context, string, string, string) (agentdomain.Task, string, error) {
	return agentdomain.Task{}, "", agentdomain.ErrNotFound
}

func (f *fakeAgents) AppTaskEvents(context.Context, string, string, string, int64, int) ([]agentdomain.Event, error) {
	return nil, agentdomain.ErrNotFound
}

type fakeProjects struct {
	project       projectdomain.Project
	err           error
	gets          int
	lastOwnerID   string
	lastProjectID string
}

func (f *fakeProjects) Get(_ context.Context, ownerID, projectID string) (projectdomain.Project, error) {
	f.gets++
	f.lastOwnerID, f.lastProjectID = ownerID, projectID
	return f.project, f.err
}

func TestTaskRouterResolvesProviderSnapshots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		projectID string
		project   projectdomain.Project
		want      string
	}{
		{name: "global default", want: "fake"},
		{name: "project without binding", projectID: "project-1", want: "fake"},
		{
			name: "project binding", projectID: "project-1", want: "deepseek",
			project: projectdomain.Project{HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "deepseek"}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			agents := &fakeAgents{}
			projects := &fakeProjects{project: test.project}
			router, err := NewTaskRouter(agents, projects, "fake")
			if err != nil {
				t.Fatal(err)
			}
			task, err := router.Submit(context.Background(), agentapp.SubmitInput{
				OwnerUserID: "owner", IdempotencyKey: "key", ProjectID: test.projectID, Payload: []byte(`{"goal":"test"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if task.ProviderID != test.want || len(agents.submitted) != 1 || agents.submitted[0].ProviderID != test.want {
				t.Fatalf("provider snapshot = %q, submitted = %#v", task.ProviderID, agents.submitted)
			}
			if test.projectID != "" && (projects.lastOwnerID != "owner" || projects.lastProjectID != test.projectID) {
				t.Fatalf("project lookup was not owner scoped: owner=%q project=%q", projects.lastOwnerID, projects.lastProjectID)
			}
		})
	}
}

func TestTaskRouterReturnsIdempotentTaskBeforeProjectLookup(t *testing.T) {
	t.Parallel()
	existing := agentdomain.Task{ID: "existing", ProviderID: "deepseek"}
	agents := &fakeAgents{existing: &existing}
	projects := &fakeProjects{
		project: projectdomain.Project{HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "fake"}},
	}
	router, err := NewTaskRouter(agents, projects, "fake")
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.Submit(context.Background(), agentapp.SubmitInput{
		OwnerUserID: "owner", IdempotencyKey: "same-key", ProjectID: "project-1", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != existing.ID || task.ProviderID != "deepseek" || projects.gets != 0 || len(agents.submitted) != 0 {
		t.Fatalf("idempotent route changed: task=%#v project_gets=%d submits=%d", task, projects.gets, len(agents.submitted))
	}
}

func TestTaskRouterRejectsInaccessibleOrArchivedProject(t *testing.T) {
	t.Parallel()
	archivedAt := time.Now().UTC()
	tests := []struct {
		name     string
		projects *fakeProjects
	}{
		{name: "missing", projects: &fakeProjects{err: projectdomain.ErrNotFound}},
		{name: "invalid scope", projects: &fakeProjects{err: projectdomain.ErrInvalid}},
		{name: "archived", projects: &fakeProjects{project: projectdomain.Project{ArchivedAt: &archivedAt}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			router, err := NewTaskRouter(&fakeAgents{}, test.projects, "fake")
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Submit(context.Background(), agentapp.SubmitInput{
				OwnerUserID: "owner", IdempotencyKey: "key", ProjectID: "project-1", Payload: []byte(`{}`),
			})
			if !errors.Is(err, agentdomain.ErrProjectDenied) {
				t.Fatalf("expected project denial, got %v", err)
			}
		})
	}
}

func TestTaskRouterSubmitForAppReplaysBeforeProjectLookup(t *testing.T) {
	t.Parallel()
	first := agentdomain.Task{ID: "first-app-task", ProviderID: "bound"}
	agents := &fakeAgents{appReplayTask: &first}
	projects := &fakeProjects{}
	router, err := NewTaskRouter(agents, projects, "fake")
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if err != nil || task.ID != "first-app-task" || task.ProviderID != "bound" {
		t.Fatalf("replay failed: %v %+v", err, task)
	}
	if len(agents.appSubmitted) != 0 {
		t.Fatal("replay must not resubmit")
	}
	if projects.gets != 0 {
		t.Fatal("replay must not consult the project")
	}
}

func TestTaskRouterSubmitForAppConflictsOnDifferentRequest(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{appReplayDigestDiffers: true}
	router, err := NewTaskRouter(agents, &fakeProjects{}, "fake")
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if !errors.Is(err, agentdomain.ErrIdempotencyConflict) {
		t.Fatalf("different-request verdict: %v", err)
	}
}

func TestTaskRouterSubmitForAppSnapshotsProjectProvider(t *testing.T) {
	t.Parallel()
	agents := &fakeAgents{}
	projects := &fakeProjects{project: projectdomain.Project{
		ID: "project-1", OwnerUserID: "owner-1",
		HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "deepseek"},
	}}
	router, err := NewTaskRouter(agents, projects, "fake")
	if err != nil {
		t.Fatal(err)
	}
	task, err := router.SubmitForApp(context.Background(), agentapp.AppSubmitInput{
		OwnerUserID: "owner-1", AppInstanceID: "instance-1", ClientIdempotencyKey: "key-1",
		RequestDigest: agentdomain.AppTaskRequestDigest("role", "goal"),
		ProjectID:     "project-1", Role: "role", Goal: "goal",
	})
	if err != nil || task.ProviderID != "deepseek" {
		t.Fatalf("provider snapshot missing: %v %+v", err, task)
	}
	if len(agents.appSubmitted) != 1 || agents.appSubmitted[0].ProviderID != "deepseek" {
		t.Fatalf("submission missing provider snapshot: %+v", agents.appSubmitted)
	}
	if agents.appSubmitted[0].RequestDigest != agentdomain.AppTaskRequestDigest("role", "goal") {
		t.Fatal("digest not forwarded")
	}
}
