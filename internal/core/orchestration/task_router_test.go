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
	existing  *agentdomain.Task
	submitted []agentapp.SubmitInput
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
