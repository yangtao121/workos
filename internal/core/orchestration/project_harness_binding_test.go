package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	catalogdomain "github.com/yangtao121/workos/internal/core/harnesscatalog/domain"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
)

type bindingProjectsFake struct {
	project    projectdomain.Project
	getErr     error
	updateErr  error
	lastOwner  string
	lastID     string
	lastUpdate projectapp.UpdateInput
	updates    int
}

func (f *bindingProjectsFake) Get(_ context.Context, ownerID, projectID string) (projectdomain.Project, error) {
	f.lastOwner, f.lastID = ownerID, projectID
	return f.project, f.getErr
}

func (f *bindingProjectsFake) Update(_ context.Context, input projectapp.UpdateInput) (projectdomain.Project, error) {
	f.updates++
	f.lastUpdate = input
	if f.updateErr != nil {
		return projectdomain.Project{}, f.updateErr
	}
	updated := f.project
	updated.Revision = input.ExpectedRevision + 1
	if input.ClearHarnessBinding {
		updated.HarnessBinding = nil
	} else if input.HarnessBinding != nil {
		binding := *input.HarnessBinding
		updated.HarnessBinding = &binding
	}
	f.project = updated
	return updated, nil
}

type bindingCatalogFake struct {
	catalog catalogdomain.Catalog
	err     error
	gets    int
}

func (f *bindingCatalogFake) Get(context.Context) (catalogdomain.Catalog, error) {
	f.gets++
	return f.catalog, f.err
}

func newBindingTestBinder(t *testing.T, projects *bindingProjectsFake, catalog *bindingCatalogFake) *ProjectHarnessBinder {
	t.Helper()
	binder, err := NewProjectHarnessBinder(projects, catalog, stubBindingCredentials{}, BindingPreset{
		InstancePolicy: "ephemeral", ProfileID: "general", ResourcePolicyID: "project-no-tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	return binder
}

func TestProjectHarnessBinderAllowsHealthyAndDegradedProviders(t *testing.T) {
	t.Parallel()
	for _, health := range []catalogdomain.Health{catalogdomain.HealthHealthy, catalogdomain.HealthDegraded} {
		projects := &bindingProjectsFake{project: projectdomain.Project{ID: "project-1", OwnerUserID: "owner-1", Revision: 4}}
		catalog := &bindingCatalogFake{catalog: catalogdomain.Catalog{Providers: []catalogdomain.Provider{{ID: "selected", Health: health}}}}
		updated, err := newBindingTestBinder(t, projects, catalog).Set(context.Background(), SetProjectHarnessBindingInput{
			OwnerUserID: "owner-1", ProjectID: "project-1", ExpectedRevision: 4, ProviderID: "selected",
		})
		if err != nil {
			t.Fatal(err)
		}
		binding := updated.HarnessBinding
		if projects.lastOwner != "owner-1" || projects.lastID != "project-1" || binding == nil || binding.ProviderID != "selected" || binding.InstancePolicy != "ephemeral" || binding.ProfileID != "general" || binding.ResourcePolicyID != "project-no-tools" || binding.CredentialRef != "" || updated.Revision != 5 {
			t.Fatalf("unsafe or incorrectly scoped binding: project=%#v input=%#v", updated, projects.lastUpdate)
		}
	}
}

func TestProjectHarnessBinderRejectsUnknownAndUnselectableProviders(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		providers []catalogdomain.Provider
		want      error
	}{
		{name: "unknown", want: ErrProviderUnknown},
		{name: "starting", providers: []catalogdomain.Provider{{ID: "selected", Health: catalogdomain.HealthStarting}}, want: ErrProviderNotSelectable},
		{name: "unavailable", providers: []catalogdomain.Provider{{ID: "selected", Health: catalogdomain.HealthUnavailable}}, want: ErrProviderNotSelectable},
		{name: "unknown health", providers: []catalogdomain.Provider{{ID: "selected", Health: catalogdomain.HealthUnknown}}, want: ErrProviderNotSelectable},
	} {
		t.Run(test.name, func(t *testing.T) {
			projects := &bindingProjectsFake{project: projectdomain.Project{ID: "project-1", Revision: 1}}
			catalog := &bindingCatalogFake{catalog: catalogdomain.Catalog{Providers: test.providers}}
			_, err := newBindingTestBinder(t, projects, catalog).Set(context.Background(), SetProjectHarnessBindingInput{
				OwnerUserID: "owner-1", ProjectID: "project-1", ExpectedRevision: 1, ProviderID: "selected",
			})
			if !errors.Is(err, test.want) || projects.updates != 0 {
				t.Fatalf("expected %v without update, got %v updates=%d", test.want, err, projects.updates)
			}
		})
	}
}

func TestProjectHarnessBinderClearsWithoutCatalog(t *testing.T) {
	t.Parallel()
	projects := &bindingProjectsFake{project: projectdomain.Project{
		ID: "project-1", Revision: 2,
		HarnessBinding: &projectdomain.HarnessBinding{ProviderID: "missing", InstancePolicy: "ephemeral", ResourcePolicyID: "old"},
	}}
	catalog := &bindingCatalogFake{err: catalogdomain.ErrUnavailable}
	updated, err := newBindingTestBinder(t, projects, catalog).Set(context.Background(), SetProjectHarnessBindingInput{
		OwnerUserID: "owner-1", ProjectID: "project-1", ExpectedRevision: 2, UseGlobalDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.gets != 0 || updated.HarnessBinding != nil || !projects.lastUpdate.ClearHarnessBinding {
		t.Fatalf("clear depended on catalog or retained binding: catalog_gets=%d project=%#v", catalog.gets, updated)
	}
}

func TestProjectHarnessBinderPreservesProjectFailureSemantics(t *testing.T) {
	t.Parallel()
	archivedAt := time.Now().UTC()
	for _, test := range []struct {
		name     string
		projects *bindingProjectsFake
		want     error
	}{
		{name: "other owner or missing", projects: &bindingProjectsFake{getErr: projectdomain.ErrNotFound}, want: projectdomain.ErrNotFound},
		{name: "archived", projects: &bindingProjectsFake{project: projectdomain.Project{Revision: 1, ArchivedAt: &archivedAt}}, want: projectdomain.ErrConflict},
		{name: "revision conflict", projects: &bindingProjectsFake{project: projectdomain.Project{Revision: 2}}, want: projectdomain.ErrConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &bindingCatalogFake{catalog: catalogdomain.Catalog{Providers: []catalogdomain.Provider{{ID: "fake", Health: catalogdomain.HealthHealthy}}}}
			_, err := newBindingTestBinder(t, test.projects, catalog).Set(context.Background(), SetProjectHarnessBindingInput{
				OwnerUserID: "owner-1", ProjectID: "project-1", ExpectedRevision: 1, ProviderID: "fake",
			})
			if !errors.Is(err, test.want) || catalog.gets != 0 || test.projects.updates != 0 {
				t.Fatalf("unexpected project failure: err=%v catalog_gets=%d updates=%d", err, catalog.gets, test.projects.updates)
			}
		})
	}
}

func TestProjectHarnessBinderPropagatesCatalogFailureOnlyForBind(t *testing.T) {
	t.Parallel()
	projects := &bindingProjectsFake{project: projectdomain.Project{ID: "project-1", Revision: 1}}
	catalog := &bindingCatalogFake{err: catalogdomain.ErrUnavailable}
	_, err := newBindingTestBinder(t, projects, catalog).Set(context.Background(), SetProjectHarnessBindingInput{
		OwnerUserID: "owner-1", ProjectID: "project-1", ExpectedRevision: 1, ProviderID: "fake",
	})
	if !errors.Is(err, catalogdomain.ErrUnavailable) || catalog.gets != 1 || projects.updates != 0 {
		t.Fatalf("unexpected catalog failure behavior: err=%v gets=%d updates=%d", err, catalog.gets, projects.updates)
	}
}

// stubBindingCredentials satisfies the binder's credential port: no owner
// credential resolves, so credential-requiring providers stay unbindable.
type stubBindingCredentials struct{}

func (stubBindingCredentials) ActiveSnapshot(context.Context, string, string) (agentports.CredentialSnapshotRef, error) {
	return agentports.CredentialSnapshotRef{}, agentdomain.ErrNotFound
}
