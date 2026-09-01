package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
)

type projectRepoFake struct {
	projects map[string]projectdomain.Project
	fail     bool
}

func (r *projectRepoFake) LookupCreateRequest(context.Context, string, string) (projectports.StoredCreateRequest, bool, error) {
	return projectports.StoredCreateRequest{}, false, nil
}

func (r *projectRepoFake) CreateProject(_ context.Context, command projectports.CreateCommand) (projectdomain.Project, error) {
	r.projects[command.Project.ID] = command.Project
	return command.Project, nil
}

func (r *projectRepoFake) GetProject(_ context.Context, ownerID, projectID string) (projectdomain.Project, error) {
	if r.fail {
		return projectdomain.Project{}, errors.New("database unavailable")
	}
	project, ok := r.projects[projectID]
	if !ok || project.OwnerUserID != ownerID {
		return projectdomain.Project{}, projectdomain.ErrNotFound
	}
	return project, nil
}

func (r *projectRepoFake) ListProjects(context.Context, string, string, int, bool) ([]projectdomain.Project, error) {
	return nil, nil
}

func (r *projectRepoFake) UpdateProject(_ context.Context, project projectdomain.Project, _ int64) (projectdomain.Project, error) {
	return project, nil
}

func (r *projectRepoFake) ArchiveProject(_ context.Context, ownerID, projectID string, _ int64) (projectdomain.Project, error) {
	project, err := r.GetProject(context.Background(), ownerID, projectID)
	if err != nil {
		return projectdomain.Project{}, err
	}
	archived := time.Now().UTC()
	project.ArchivedAt = &archived
	return project, nil
}

func TestProjectDirectoryMapsProjectOutcomes(t *testing.T) {
	t.Parallel()
	active := projectdomain.Project{ID: "01999999-9999-7999-8999-999999999991", OwnerUserID: "owner-1"}
	archived := time.Now().UTC()
	repository := &projectRepoFake{projects: map[string]projectdomain.Project{
		active.ID:                              active,
		"01999999-9999-7999-8999-999999999992": {ID: "01999999-9999-7999-8999-999999999992", OwnerUserID: "owner-1", ArchivedAt: &archived},
		"01999999-9999-7999-8999-999999999993": {ID: "01999999-9999-7999-8999-999999999993", OwnerUserID: "owner-2"},
	}}
	directory, err := NewProjectDirectory(projectapp.New(repository, staticIDGenerator{}))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := directory.Get(context.Background(), "owner-1", active.ID); err != nil {
		t.Fatalf("active owned project must be usable: %v", err)
	}
	for name, arguments := range map[string][2]string{
		"unknown":    {"owner-1", "01999999-9999-7999-8999-999999999994"},
		"foreign":    {"owner-1", "01999999-9999-7999-8999-999999999993"},
		"archived":   {"owner-1", "01999999-9999-7999-8999-999999999992"},
		"malformed":  {"owner-1", "not-a-uuid"},
		"wrong-uuid": {"owner-1", "01999999-9999-4999-8999-999999999995"},
	} {
		if _, err := directory.Get(context.Background(), arguments[0], arguments[1]); !errors.Is(err, appregistryapp.ErrProjectDenied) {
			t.Fatalf("%s project must be denied, got %v", name, err)
		}
	}

	repository.fail = true
	if _, err := directory.Get(context.Background(), "owner-1", active.ID); err == nil || errors.Is(err, appregistryapp.ErrProjectDenied) {
		t.Fatalf("infrastructure failure must surface as an internal error, got %v", err)
	}
}

type staticIDGenerator struct{}

func (staticIDGenerator) New() string { return "01999999-9999-7999-8999-999999999999" }

func (r *projectRepoFake) ReconcileArchivedProjectsPage(context.Context, string, int) ([]projectports.ArchivedProjectRef, string, error) {
	return nil, "", errors.New("not used in this test")
}
