package ports

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/core/artifact/domain"
)

// ErrStoreUnavailable marks a temporarily unreachable artifact store. The
// postgres adapter wraps transient driver failures with it at the port
// boundary; transports map it to a sanitized Unavailable. Invariant and
// constraint failures keep their own verdicts and stay Internal.
var ErrStoreUnavailable = errors.New("artifact store is temporarily unavailable")

// PageResult is the explicit paging contract: items plus the next cursor as
// decided by the repository probe.
type PageResult struct {
	Items     []domain.Artifact
	NextToken string
}

// CreateCommand is one fully validated create command: the server-generated
// identity and the normalized bundle. The repository persists metadata, all
// files, and the idempotency mapping in one transaction.
type CreateCommand struct {
	Artifact       domain.Artifact
	Bundle         domain.WebBundle
	IdempotencyKey string
	RequestDigest  string
}

// Repository owns the immutable bundle facts. Same-key races are arbitrated
// by the request-mapping primary key inside the create transaction, never by
// process state.
type Repository interface {
	// Create persists one immutable artifact or resolves an already-consumed
	// key: the identical canonical request replays the stored artifact, a
	// different one returns ErrIdempotencyConflict.
	Create(ctx context.Context, command CreateCommand) (domain.Artifact, error)
	// Get reads one owner-scoped artifact's metadata (never file bytes).
	Get(ctx context.Context, ownerUserID, artifactID string) (domain.Artifact, error)
	// ListIDsPage returns owner artifact IDs ordered after the cursor,
	// probing one row beyond the limit for the next cursor.
	ListIDsPage(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, string, error)
	// VisitSummaries streams the metadata of exactly the given IDs.
	VisitSummaries(ctx context.Context, ownerUserID string, ids []string, visit func(domain.Artifact) error) error
	// ReadAsset returns one stored file (bytes, media type, etag) or
	// ErrNotFound when the artifact has no such normalized path.
	ReadAsset(ctx context.Context, ownerUserID, artifactID, path string) (domain.BundleFile, error)
}
