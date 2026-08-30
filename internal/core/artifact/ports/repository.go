package ports

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
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

// ReviewOutputCommand is one fully validated review materialization insert:
// the server-minted artifact identity, the normalized content bytes, the
// request digest that adjudicates replay versus conflict, and the Core-minted
// publication record. The coordinator persists it inside one shared
// transaction together with the Agent module's timeline event.
type ReviewOutputCommand struct {
	Artifact      domain.ReviewArtifact
	Content       []byte
	RequestDigest string
	Publication   domain.PublicationRecord
}

// TaskOutputRecord is one stored adjudication read: which canonical request
// consumed a (task, output key) identity, which artifact it minted, and the
// exact first-published timeline event reference.
type TaskOutputRecord struct {
	RequestDigest string
	ArtifactID    string
	ArtifactType  string
	Publication   domain.PublicationRecord
}

// ProjectScope is the neutral project liveness port for review artifact
// reads. The Artifact module never imports Project adapters or SQL; the
// orchestration layer adapts the Project service to this port.
type ProjectScope interface {
	// ValidateReadableProject proves projectID is an existing project owned
	// by ownerUserID. Archived projects stay readable — review artifacts are
	// immutable history; unknown or foreign projects fail closed without
	// existence disclosure.
	ValidateReadableProject(ctx context.Context, ownerUserID, projectID string) error
}

// Repository owns the immutable artifact facts of both implemented subtypes.
// Same-key races are arbitrated by unique constraints inside the write
// transaction, never by process state. Metadata reads span both subtypes;
// review content reads are typed and owner-scoped.
type Repository interface {
	// Create persists one immutable web bundle artifact or resolves an
	// already-consumed key: the identical canonical request replays the
	// stored artifact, a different one returns ErrIdempotencyConflict.
	Create(ctx context.Context, command CreateCommand) (domain.Artifact, error)
	// Get reads one owner-scoped artifact's metadata (never content bytes)
	// across both implemented subtypes.
	Get(ctx context.Context, ownerUserID, artifactID string) (domain.Artifact, error)
	// ListIDsPage returns owner artifact IDs ordered after the cursor across
	// both subtypes, probing one row beyond the limit for the next cursor.
	ListIDsPage(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, string, error)
	// VisitSummaries streams the metadata of exactly the given IDs.
	VisitSummaries(ctx context.Context, ownerUserID string, ids []string, visit func(domain.Artifact) error) error
	// ReadAsset returns one stored bundle file (bytes, media type, etag) or
	// ErrNotFound when the artifact has no such normalized path.
	ReadAsset(ctx context.Context, ownerUserID, artifactID, path string) (domain.BundleFile, error)

	// GetReviewContent reads one owner-scoped review artifact's metadata and
	// exact canonical content bytes from the same row snapshot. Unknown
	// subtypes (including web bundles) are ErrNotFound.
	GetReviewContent(ctx context.Context, ownerUserID, artifactID string) (domain.ReviewArtifact, domain.NormalizedReviewContent, error)
	// ListProjectReviewIDsPage pages one owner project's review artifact IDs
	// in UUIDv7 order with a one-row probe for the next cursor.
	ListProjectReviewIDsPage(ctx context.Context, ownerUserID, projectID, cursor string, limit int) ([]string, string, error)

	// FindTaskOutput reads one adjudication mapping inside the coordinator's
	// transaction.
	FindTaskOutput(ctx context.Context, tx dbtx.Tx, taskID, outputKey string) (TaskOutputRecord, bool, error)
	// InsertTaskOutput persists the immutable artifact row and the
	// adjudication mapping inside the coordinator's transaction. Zero rows
	// means a concurrent winner consumed the (task, output key) identity or
	// the (task, type) slot; the caller re-classifies with FindTaskOutput.
	InsertTaskOutput(ctx context.Context, tx dbtx.Tx, command ReviewOutputCommand) (int64, error)
	// ReviewArtifactByID reads one stored review artifact row inside the
	// coordinator's transaction; the caller validates the lease-derived
	// owner/project/task binding.
	ReviewArtifactByID(ctx context.Context, tx dbtx.Tx, artifactID string) (domain.ReviewArtifact, error)
}
