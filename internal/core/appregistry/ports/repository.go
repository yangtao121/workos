package ports

import (
	"context"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

// Repository persists immutable App versions and the authoritative
// registration-request idempotency mapping. Register must rely on database
// constraints to decide concurrent registrations: a replay of the same
// (owner, app, version, digest) or of the same idempotency request returns
// the stored record, while conflicting writes surface the domain conflict
// errors. Public read paths stream bounded summaries and never materialize
// canonical manifests.
type Repository interface {
	Register(context.Context, domain.AppVersion) (domain.AppVersionSummary, error)
	GetVersion(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersionSummary, error)
	// ListAppIDPage returns at most limit distinct app IDs after cursor plus
	// the next cursor, or an empty next cursor when no further apps exist. The
	// implementation probes limit+1 rows so it never fabricates a cursor for an
	// exactly-full final page.
	ListAppIDPage(ctx context.Context, ownerUserID, cursor string, limit int) (appIDs []string, nextCursor string, err error)
	// VisitVersionSummaries streams the summary projection of every version of
	// the given apps, grouped and ordered by app ID, so callers can fold
	// current versions with a fixed-size accumulator instead of materializing
	// all rows.
	VisitVersionSummaries(ctx context.Context, ownerUserID string, appIDs []string, visit func(domain.AppVersionSummary) error) error
}
