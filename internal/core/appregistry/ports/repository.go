package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

// Repository persists immutable App versions. Register must rely on database
// constraints to decide concurrent registrations: a replay of the same
// (owner, app, version, digest) or of the same idempotency request returns
// the stored record, while conflicting writes surface the domain conflict
// errors.
type Repository interface {
	Register(context.Context, domain.AppVersion) (domain.AppVersion, error)
	GetVersion(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersion, error)
	GetAppVersions(ctx context.Context, ownerUserID, appID string) ([]domain.AppVersion, error)
	ListAppIDs(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, error)
	GetVersionsForApps(ctx context.Context, ownerUserID string, appIDs []string) ([]domain.AppVersion, error)
}
