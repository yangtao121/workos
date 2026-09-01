// Ports of the Core index publication feed. The transaction-scoped sink is
// the only way another Core module appends a publication: it always joins the
// caller's source-mutation transaction, so a publication commits exactly when
// the artifact or archive fact commits (ADR-0013). The claim store and the
// source authority are consumed by the feed application service only.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/indexfeed/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// ErrStoreUnavailable marks a temporarily unreachable publication store.
var ErrStoreUnavailable = errors.New("index publication store is temporarily unavailable")

// Source re-verification verdicts. The application service classifies them;
// none of them can be produced by consumer input.
var (
	// ErrSourceArchived: the authoritative lifecycle says the project is
	// archived, so the source must not become searchable.
	ErrSourceArchived = errors.New("index source project is archived")
	// ErrSourceCorrupt: the publication's ref no longer resolves to the
	// exact claimed immutable identity/digest — stored drift.
	ErrSourceCorrupt = errors.New("index source failed re-verification")
	// ErrSourceUnsupported: the source is not an implemented review subtype.
	ErrSourceUnsupported = errors.New("index source is not an implemented review subtype")
)

// TxSink appends publication facts inside the caller's source-mutation
// transaction. Zero remaining rows on insert means a unique arbitration
// would have been violated — for upserts (one publication per immutable
// artifact) and tombstones (one per project lifetime) both are corruption,
// not business events.
type TxSink interface {
	AppendReviewArtifactUpsert(ctx context.Context, tx dbtx.Tx, publication domain.Publication) error
	AppendProjectTombstone(ctx context.Context, tx dbtx.Tx, publication domain.Publication) error
}

// ClaimedPublication is one lease held by this worker.
type ClaimedPublication struct {
	Publication domain.Publication
	LeaseToken  string
	ExpiresAt   time.Time
}

// PublicationStore is the durable claim/complete authority over pending
// publications.
type PublicationStore interface {
	// ClaimPending leases up to maxBatch pending publications to workerID
	// until leaseUntil (database-arbitrated: FOR UPDATE SKIP LOCKED, so two
	// workers can never hold the same live lease).
	ClaimPending(ctx context.Context, workerID, leaseToken string, leaseUntil, now time.Time, maxBatch int) ([]ClaimedPublication, error)
	// LockForResolve locks one claimed publication inside the caller's
	// transaction and revalidates the live lease. Any mismatch is
	// ErrLeaseStale.
	LockForResolve(ctx context.Context, tx dbtx.Tx, publicationID, workerID, leaseToken string, now time.Time) (domain.Publication, error)
	// Complete records terminal outcomes for the given live claims inside the
	// caller's transaction and returns the ids that were actually acked by
	// this worker (stale claims are absent).
	Complete(ctx context.Context, tx dbtx.Tx, workerID string, results []CompleteResult, now time.Time) (map[string]bool, error)
}

// CompleteResult is one outcome the consumer recorded locally.
type CompleteResult struct {
	PublicationID string
	LeaseToken    string
	Outcome       string
}

// VerifiedSource is the canonical bounded snapshot of one review artifact
// after full re-verification against Core authority.
type VerifiedSource struct {
	OwnerUserID  string
	ProjectID    string
	ArtifactID   string
	SourceTaskID string
	ArtifactType string
	Digest       string
	Title        string
	Content      []byte
	CreatedAt    time.Time
}

// SourceSummary is one authoritative active-project review artifact fact
// without content (reconciliation pages).
type SourceSummary struct {
	OwnerUserID  string
	ProjectID    string
	ArtifactID   string
	ArtifactType string
	Digest       string
	CreatedAt    time.Time
}

// ArchivedProject is one archived project scope.
type ArchivedProject struct {
	OwnerUserID string
	ProjectID   string
	ArchivedAt  time.Time
}

// SourcePage is one explicit reconciliation page plus its continuation.
type SourcePage struct {
	Sources    []SourceSummary
	NextToken  string
	AppendMore bool
}

// SourceAuthority is the neutral authority port over the Artifact and
// Project modules. Implemented in the orchestration layer; indexfeed never
// imports other modules or queries their tables.
type SourceAuthority interface {
	// ResolveReviewSource re-verifies one immutable review artifact inside the
	// caller's transaction: implemented subtype, exact owner/project/digest
	// binding, canonical content, and the project still active. Verified
	// content leaves Core exactly once per resolve.
	ResolveReviewSource(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID, artifactID, expectedDigest string) (VerifiedSource, error)
	// ReconcileSources pages authoritative active-project review artifacts in
	// stable (created_at, id) order. Malformed cursors fail closed.
	ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]SourceSummary, string, error)
	// ReconcileArchivedProjects pages archived project scopes in stable order.
	ReconcileArchivedProjects(ctx context.Context, pageSize int, cursor string) ([]ArchivedProject, string, error)
	// ResolveSourceContent resolves bounded verified content for a specific
	// authoritative artifact (digest-pinned) outside the claim path.
	ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (VerifiedSource, error)
}
