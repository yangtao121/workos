package orchestration

import (
	"context"
	"errors"
	"fmt"

	appregistryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	appregistryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
)

// AppCatalog adapts the App Registry application service to the Project
// module's neutral catalog port. It is the only bridge in this direction:
// the Project application never imports registry adapters, SQL, or
// manifests, and the registry never learns about installations.
type AppCatalog struct {
	apps *appregistryapp.Service
}

func NewAppCatalog(apps *appregistryapp.Service) (*AppCatalog, error) {
	if apps == nil {
		return nil, errors.New("app catalog requires the app registry service")
	}
	return &AppCatalog{apps: apps}, nil
}

// Resolve returns the immutable registry reference for an install command:
// an empty version resolves the registry's current version inside this
// call, an explicit version returns that exact immutable version. Unknown
// or foreign apps are denials, not existence leaks. A transient registry
// store outage surfaces as the Project module's dependency-unavailable
// sentinel so the neutral port's consumers can answer a retryable
// Unavailable without importing registry internals.
func (c *AppCatalog) Resolve(ctx context.Context, ownerUserID, appID, version string) (projectdomain.PinnedApp, error) {
	summary, err := c.apps.Get(ctx, ownerUserID, appID, version)
	switch {
	case errors.Is(err, appregistrydomain.ErrNotFound):
		return projectdomain.PinnedApp{}, projectapp.ErrAppNotInstallable
	case errors.Is(err, appregistrydomain.ErrInvalid):
		return projectdomain.PinnedApp{}, projectdomain.ErrInvalid
	case errors.Is(err, appregistryports.ErrStoreUnavailable):
		return projectdomain.PinnedApp{}, fmt.Errorf("resolve app for installation: %w: %w", projectports.ErrStoreUnavailable, err)
	case err != nil:
		return projectdomain.PinnedApp{}, fmt.Errorf("resolve app for installation: %w", err)
	}
	return projectdomain.PinnedApp{
		AppID: summary.AppID, Version: summary.Version,
		ManifestDigest: summary.ManifestDigest, Scope: string(summary.Scope),
		// Requested permissions travel as the subset boundary for grant
		// validation; they never become a grant by themselves.
		Permissions: summary.Permissions,
	}, nil
}
