package orchestration

import (
	"context"
	"errors"
	"strings"

	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

var (
	ErrProviderUnknown       = errors.New("selected harness provider is not in the catalog")
	ErrProviderNotSelectable = errors.New("selected harness provider is not available for new bindings")
)

type BindingProjects interface {
	Get(context.Context, string, string) (projectdomain.Project, error)
	Update(context.Context, projectapp.UpdateInput) (projectdomain.Project, error)
}

type ProviderCatalog interface {
	Get(context.Context) (catalogdomain.Catalog, error)
}

type BindingPreset struct {
	InstancePolicy   string
	ProfileID        string
	ResourcePolicyID string
}

type SetProjectHarnessBindingInput struct {
	OwnerUserID      string
	ProjectID        string
	ExpectedRevision int64
	ProviderID       string
	UseGlobalDefault bool
}

type ProjectHarnessBinder struct {
	projects BindingProjects
	catalog  ProviderCatalog
	preset   BindingPreset
}

func NewProjectHarnessBinder(projects BindingProjects, catalog ProviderCatalog, preset BindingPreset) (*ProjectHarnessBinder, error) {
	preset.InstancePolicy = strings.TrimSpace(preset.InstancePolicy)
	preset.ProfileID = strings.TrimSpace(preset.ProfileID)
	preset.ResourcePolicyID = strings.TrimSpace(preset.ResourcePolicyID)
	validation := &projectdomain.HarnessBinding{
		ProviderID: "validation", InstancePolicy: preset.InstancePolicy,
		ProfileID: preset.ProfileID, ResourcePolicyID: preset.ResourcePolicyID,
	}
	if projects == nil || catalog == nil || projectdomain.ValidateBinding(validation) != nil {
		return nil, errors.New("project harness binder requires project, catalog, and valid preset dependencies")
	}
	return &ProjectHarnessBinder{projects: projects, catalog: catalog, preset: preset}, nil
}

func (b *ProjectHarnessBinder) Set(ctx context.Context, input SetProjectHarnessBindingInput) (projectdomain.Project, error) {
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	hasProvider := input.ProviderID != ""
	if input.OwnerUserID == "" || input.ProjectID == "" || input.ExpectedRevision <= 0 || hasProvider == input.UseGlobalDefault {
		return projectdomain.Project{}, projectdomain.ErrInvalid
	}
	project, err := b.projects.Get(ctx, input.OwnerUserID, input.ProjectID)
	if err != nil {
		return projectdomain.Project{}, err
	}
	if project.ArchivedAt != nil || project.Revision != input.ExpectedRevision {
		return projectdomain.Project{}, projectdomain.ErrConflict
	}
	update := projectapp.UpdateInput{
		OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, ExpectedRevision: input.ExpectedRevision,
	}
	if input.UseGlobalDefault {
		update.ClearHarnessBinding = true
		return b.projects.Update(ctx, update)
	}

	catalog, err := b.catalog.Get(ctx)
	if err != nil {
		return projectdomain.Project{}, err
	}
	var selected *catalogdomain.Provider
	for index := range catalog.Providers {
		if catalog.Providers[index].ID == input.ProviderID {
			selected = &catalog.Providers[index]
			break
		}
	}
	if selected == nil {
		return projectdomain.Project{}, ErrProviderUnknown
	}
	if selected.Health != catalogdomain.HealthHealthy && selected.Health != catalogdomain.HealthDegraded {
		return projectdomain.Project{}, ErrProviderNotSelectable
	}
	update.HarnessBinding = &projectdomain.HarnessBinding{
		ProviderID: input.ProviderID, InstancePolicy: b.preset.InstancePolicy,
		ProfileID: b.preset.ProfileID, ResourcePolicyID: b.preset.ResourcePolicyID,
	}
	return b.projects.Update(ctx, update)
}
