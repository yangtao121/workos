package orchestration

import (
	"context"
	"errors"
	"fmt"

	appregistrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	appregistryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
)

// ResolveStaged adapts the Registry's any-state read to the Project module's
// staged canary port (ADR-0026). It never serves owner-facing resolution.
func (c *AppCatalog) ResolveStaged(ctx context.Context, ownerUserID, appID, version string) (projectdomain.PinnedApp, error) {
	summary, err := c.apps.GetStaged(ctx, ownerUserID, appID, version)
	switch {
	case errors.Is(err, appregistrydomain.ErrNotFound):
		return projectdomain.PinnedApp{}, projectapp.ErrAppNotInstallable
	case errors.Is(err, appregistrydomain.ErrInvalid):
		return projectdomain.PinnedApp{}, projectdomain.ErrInvalid
	case errors.Is(err, appregistryports.ErrStoreUnavailable):
		return projectdomain.PinnedApp{}, fmt.Errorf("resolve staged app: %w: %w", projectports.ErrStoreUnavailable, err)
	case err != nil:
		return projectdomain.PinnedApp{}, fmt.Errorf("resolve staged app: %w", err)
	}
	return projectdomain.PinnedApp{
		AppID: summary.AppID, Version: summary.Version,
		ManifestDigest: summary.ManifestDigest, Scope: string(summary.Scope),
		Permissions: summary.Permissions,
	}, nil
}
