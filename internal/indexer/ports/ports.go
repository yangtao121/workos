// Ports of the indexer projection. The projection repository owns the whole
// workos_index schema; the Core feed client is the only door to Core
// authority. The indexer never queries Core schemas and Core never queries
// this one (ADR-0013).
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
)

var (
	// ErrStoreUnavailable marks a temporarily unreachable projection store.
	ErrStoreUnavailable = errors.New("indexer projection store is temporarily unavailable")
	// ErrCoreUnavailable marks a temporarily unreachable Core feed.
	ErrCoreUnavailable = errors.New("index publication source is temporarily unavailable")
	// ErrLeaseStale marks a resolve/complete whose claim is no longer live.
	ErrLeaseStale = errors.New("index publication claim is not live")
	// ErrNotFound marks a missing feed fact.
	ErrNotFound = errors.New("index feed fact is not available")
	// ErrProjectNotFound marks a missing or foreign project scope. Search and
	// job paths map it to the same sanitized empty/miss verdict so there is
	// no existence oracle.
	ErrProjectNotFound = errors.New("project scope is not searchable")
)

// ResolvedSource is one verified canonical source snapshot from Core.
type ResolvedSource struct {
	Embedding     domain.ModelVector // derived in application, never supplied by Core
	Verdict       string             // "resolved" | "tombstoned" | "corrupt" | "unsupported"
	Operation     string             // "review-artifact.upsert" | "project.tombstone"
	OwnerUserID   string
	ProjectID     string
	ArtifactID    string
	SourceTaskID  string
	ArtifactType  string
	Digest        string
	Title         string
	Content       []byte
	CreatedAt     time.Time
	PublicationID string
	OccurredAt    time.Time
}

// ClaimedPublication is one lease the Core feed handed this indexer.
type ClaimedPublication struct {
	PublicationID string
	Operation     string
	OwnerUserID   string
	ProjectID     string
	SourceID      string
	ArtifactType  string
	Digest        string
	OccurredAt    time.Time
	LeaseToken    string
}

// CoreFeedClient is the private Core publication source contract.
type CoreFeedClient interface {
	Claim(ctx context.Context, workerID string, maxBatch int, lease time.Duration) ([]ClaimedPublication, error)
	Resolve(ctx context.Context, workerID, publicationID, leaseToken string) (ResolvedSource, error)
	Complete(ctx context.Context, workerID string, results []ConsumptionResult) ([]bool, error)
	// CountPending reports Core publications still awaiting an outcome.
	CountPending(ctx context.Context) (int64, error)
	// ReconcileSources pages authoritative active-project review artifacts.
	ReconcileSources(ctx context.Context, pageSize int, cursor string) ([]ReconcileSource, string, string, error)
	// ReconcileArchivedProjects pages archived project scopes.
	ReconcileArchivedProjects(ctx context.Context, pageSize int, cursor string) ([]ArchivedProject, string, error)
	// ResolveSourceContent resolves digest-pinned verified content.
	ResolveSourceContent(ctx context.Context, ownerUserID, projectID, artifactID, expectedDigest string) (ResolvedSource, error)
}

// ConsumptionResult is one locally-recorded outcome.
type ConsumptionResult struct {
	PublicationID string
	LeaseToken    string
	Outcome       string
}

// ReconcileSource is one authoritative active-project review artifact fact.
type ReconcileSource struct {
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

// DocumentStatus is the reconciliation view of one projected source.
type DocumentStatus struct {
	Known      bool
	Digest     string
	Tombstoned bool
}

// AppliedDocument is the canonical document a resolved upsert projects to.
type AppliedDocument struct {
	Document        domain.Document
	RequestDigest   string
	PublicationID   string
	GenerationScope []string // generation ids this effect was recorded under
}

// ProjectionRepository owns the workos_index schema. Receipt, document
// effect, cursor, and job progress commit inside one local transaction; the
// tx-scoped methods exist so the ingestion coordinator can compose them.
type ProjectionRepository interface {
	ModelFingerprint() string
	ReadDocument(context.Context, domain.DocumentRead) (domain.Document, error)
	// ActiveGenerationID returns the generation every search reads. A missing
	// pointer before first boot is ErrNotFound.
	ActiveGenerationID(ctx context.Context) (string, error)
	// WritableGenerationIDs returns the active generation plus every building
	// generation that must mirror live effects for the given source scope.
	WritableGenerationIDs(ctx context.Context, ownerUserID, projectID string) ([]string, error)
	// EnsureBootstrapGeneration creates the first active generation when the
	// projection has none. It is a no-op after the first successful boot.
	EnsureBootstrapGeneration(ctx context.Context, now time.Time) (string, error)

	// ApplyResolvedSource projects one resolved source: document/tombstone +
	// receipts across the writable generations + consumer cursor, in one
	// local transaction. Returns the outcome recorded.
	ApplyResolvedSource(ctx context.Context, source ResolvedSource, outcome string, requestDigest string, now time.Time) error

	// DocumentStatus reports the active-generation projection state of one
	// source for reconciliation (known, digest, tombstoned).
	DocumentStatus(ctx context.Context, ownerUserID, projectID, sourceID string) (DocumentStatus, error)
	// Search runs one bounded deterministic lexical page over the active
	// generation.
	Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error)
	// SearchHybrid runs one bounded deterministic fused lexical+cosine page
	// over the active generation (ADR-0017).
	SearchHybrid(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error)
	// Freshness reads the bounded freshness projection.
	Freshness(ctx context.Context, pending int64) (domain.Freshness, error)
}

// WorkspaceSource is one owner-bound local mount (ADR-0017 §4).
type WorkspaceSource struct {
	ID              string
	OwnerUserID     string
	ProjectID       string
	RootPath        string
	Status          string // active | degraded | stopped
	DegradedReason  string
	IndexedCount    int64
	SkippedCount    int64
	TombstonedCount int64
	LastSyncedAt    time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// MountFile is one bounded file fact produced by a mount walk. SourceID is
// the deterministic per-scope document identity of the relative path.
type MountFile struct {
	Embedding domain.ModelVector // derived after the complete filesystem scan
	RelPath   string
	Title     string
	Content   []byte
	Digest    string
	SourceID  string
}

// MountSkip is one sanitized skip fact: a fixed category, never content.
type MountSkip struct {
	RelPath string
	Reason  string
}

// MountResult is one bounded walk over a mount root.
type MountResult struct {
	Files []MountFile
	Skips []MountSkip
}

// MountReader reads owner-bound local roots. It is the only filesystem
// boundary of the workspace slice; implementations must never follow
// symlinks out of the root.
type MountReader interface {
	ValidateRoot(root string) error
	Walk(ctx context.Context, root string) (MountResult, error)
}

// WorkspaceStore owns the durable workspace source lifecycle and projects
// sync passes into the search documents.
type WorkspaceStore interface {
	InsertWorkspaceSource(ctx context.Context, source WorkspaceSource) (WorkspaceSource, error)
	GetWorkspaceSource(ctx context.Context, id string) (WorkspaceSource, error)
	ListWorkspaceSources(ctx context.Context) ([]WorkspaceSource, error)
	SetWorkspaceSourceStatus(ctx context.Context, source WorkspaceSource, status, degradedReason string, now time.Time) (WorkspaceSource, error)
	StopWorkspaceSource(ctx context.Context, source WorkspaceSource, now time.Time) (WorkspaceSource, error)
	ConvergeWorkspacePass(ctx context.Context, source WorkspaceSource, files []MountFile, skipped int64, passPublication func() string, now time.Time) (updated WorkspaceSource, applied, tombstoned int64, err error)
}

// ArchiveObject is one bounded archive object fact (ADR-0017 §5).
type ArchiveObject struct {
	ID          string
	OwnerUserID string
	Sha256      string
	MediaType   string
	ByteCount   int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ArchiveStore owns the bounded content-addressed archive.
type ArchiveStore interface {
	PutArchiveObject(ctx context.Context, ownerUserID, digest, mediaType string, content []byte, now time.Time) (ArchiveObject, bool, error)
	GetArchiveObject(ctx context.Context, ownerUserID, objectID string) (ArchiveObject, []byte, error)
	ListArchiveObjects(ctx context.Context, ownerUserID string, limit int) ([]ArchiveObject, error)
	CountArchiveObjects(ctx context.Context, ownerUserID string) (int64, error)
}
