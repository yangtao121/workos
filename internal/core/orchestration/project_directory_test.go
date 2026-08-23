package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type projectRepoFake struct {
	projects map[string]projectdomain.Project
	fail     bool
}

func (r *projectRepoFake) Create(_ context.Context, project projectdomain.Project, _ string) (projectdomain.Project, error) {
	r.projects[project.ID] = project
	return project, nil
}

func (r *projectRepoFake) Get(_ context.Context, ownerID, projectID string) (projectdomain.Project, error) {
	if r.fail {
		return projectdomain.Project{}, errors.New("database unavailable")
	}
	project, ok := r.projects[projectID]
	if !ok || project.OwnerUserID != ownerID {
		return projectdomain.Project{}, projectdomain.ErrNotFound
	}
	return project, nil
}

func (r *projectRepoFake) List(context.Context, string, string, int, bool) ([]projectdomain.Project, error) {
	return nil, nil
}

func (r *projectRepoFake) Update(_ context.Context, project projectdomain.Project, _ int64) (projectdomain.Project, error) {
	return project, nil
}

func (r *projectRepoFake) Archive(_ context.Context, ownerID, projectID string, _ int64) (projectdomain.Project, error) {
	project, err := r.Get(context.Background(), ownerID, projectID)
	if err != nil {
		return projectdomain.Project{}, err
	}
	archived := time.Now().UTC()
	project.ArchivedAt = &archived
	return project, nil
}

func TestProjectDirectoryMapsProjectOutcomes(t *testing.T) {
	t.Parallel()
	active := projectdomain.Project{ID: "p-active", OwnerUserID: "owner-1"}
	archived := time.Now().UTC()
	repository := &projectRepoFake{projects: map[string]projectdomain.Project{
		"p-active":   active,
		"p-archived": {ID: "p-archived", OwnerUserID: "owner-1", ArchivedAt: &archived},
		"p-foreign":  {ID: "p-foreign", OwnerUserID: "owner-2"},
	}}
	directory, err := NewProjectDirectory(projectapp.New(repository, staticIDGenerator{}))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := directory.Get(context.Background(), "owner-1", "p-active"); err != nil {
		t.Fatalf("active owned project must be usable: %v", err)
	}
	for name, arguments := range map[string][2]string{
		"unknown":  {"owner-1", "p-missing"},
		"foreign":  {"owner-1", "p-foreign"},
		"archived": {"owner-1", "p-archived"},
	} {
		if _, err := directory.Get(context.Background(), arguments[0], arguments[1]); !errors.Is(err, appregistryapp.ErrProjectDenied) {
			t.Fatalf("%s project must be denied, got %v", name, err)
		}
	}

	repository.fail = true
	if _, err := directory.Get(context.Background(), "owner-1", "p-active"); err == nil || errors.Is(err, appregistryapp.ErrProjectDenied) {
		t.Fatalf("infrastructure failure must surface as an internal error, got %v", err)
	}
}

type staticIDGenerator struct{}

func (staticIDGenerator) New() string { return "01999999-9999-7999-8999-999999999999" }
