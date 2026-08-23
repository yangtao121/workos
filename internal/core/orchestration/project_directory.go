package orchestration

import (
	"context"
	"errors"
	"fmt"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

// ProjectDirectory adapts the Project application service to the App
// Registry's neutral project port. It is the only bridge between the two
// modules: the registry never queries Project tables or imports Project
// adapters, and Project learns nothing about apps.
type ProjectDirectory struct {
	projects *projectapp.Service
}

func NewProjectDirectory(projects *projectapp.Service) (*ProjectDirectory, error) {
	if projects == nil {
		return nil, errors.New("project directory requires the project service")
	}
	return &ProjectDirectory{projects: projects}, nil
}

func (d *ProjectDirectory) Get(ctx context.Context, ownerUserID, projectID string) (appregistryapp.ProjectSummary, error) {
	project, err := d.projects.Get(ctx, ownerUserID, projectID)
	switch {
	case errors.Is(err, projectdomain.ErrNotFound), errors.Is(err, projectdomain.ErrInvalid):
		return appregistryapp.ProjectSummary{}, appregistryapp.ErrProjectDenied
	case err != nil:
		return appregistryapp.ProjectSummary{}, fmt.Errorf("resolve project for app registry: %w", err)
	case project.ArchivedAt != nil:
		return appregistryapp.ProjectSummary{ArchivedAt: project.ArchivedAt}, appregistryapp.ErrProjectDenied
	default:
		return appregistryapp.ProjectSummary{}, nil
	}
}
