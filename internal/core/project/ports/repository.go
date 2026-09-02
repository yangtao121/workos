package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

// ErrStoreUnavailable marks a temporarily unreachable Project store. The
// postgres adapter wraps transient driver failures with it at the port
// boundary; transports map it to a sanitized Unavailable. Invariant and
// constraint failures keep their own verdicts and stay Internal.
var ErrStoreUnavailable = errors.New("project store is temporarily unavailable")

// StoredCreateRequest is the persisted adjudication of one consumed create
// key (ADR-0004): the canonical request digest plus the versioned first
// response snapshot. The snapshot is authoritative for replays — the current,
// mutable project row is never consulted — so a replay returns the exact
// first CreateProjectResponse even after a later Update or Archive.
type StoredCreateRequest struct {
	RequestDigest string
	Result        domain.Project
}

// CreateCommand is one fully validated create command. The application has
// already normalized and validated every field, computed the canonical
// request digest, and assembled the complete first response (identifiers,
// timestamps, revision 1); the repository executes the project insert, the
// create-request adjudication, the project event, and the outbox row in one
// transaction.
type CreateCommand struct {
	Project        domain.Project
	IdempotencyKey string
	RequestDigest  string
	Now            time.Time
}

// Repository owns the Project aggregate and the create-request idempotency
// authority. Concurrent creates are arbitrated by the database: the projects
// (owner_user_id, idempotency_key) unique index decides the insert race, and
// the request-mapping primary key decides same-key adjudication across
// processes.
type Repository interface {
	// LookupCreateRequest returns the stored adjudication when the key was
	// consumed, without touching any other state.
	LookupCreateRequest(ctx context.Context, ownerUserID, idempotencyKey string) (StoredCreateRequest, bool, error)
	// CreateProject executes one create command atomically: exactly one of
	// {fresh create with event+outbox+consumed key, exact first-response
	// replay, idempotency conflict} happens, and a failure leaves nothing.
	CreateProject(ctx context.Context, command CreateCommand) (domain.Project, error)
	GetProject(ctx context.Context, ownerUserID, projectID string) (domain.Project, error)
	// ListProjects reads at most limit projects ordered by ID after the
	// cursor. The application owns the effective page size and the limit+1
	// next-page probe.
	ListProjects(ctx context.Context, ownerUserID, cursor string, limit int, includeArchived bool) ([]domain.Project, error)
	UpdateProject(ctx context.Context, project domain.Project, expectedRevision int64) (domain.Project, error)
	ArchiveProject(ctx context.Context, ownerUserID, projectID string, expectedRevision int64) (domain.Project, error)
	// ReconcileArchivedProjectsPage pages archived project scopes in stable
	// (archived_at, id) order for the index-feed tombstone convergence
	// (ADR-0013). An empty cursor opens the first page; a malformed cursor
	// is an invalid-input failure.
	ReconcileArchivedProjectsPage(ctx context.Context, cursor string, limit int) ([]ArchivedProjectRef, string, error)
}

// ArchivedProjectRef is one archived project scope fact (feed
// reconciliation): stable identity plus the authoritative archive time.
type ArchivedProjectRef struct {
	OwnerUserID string
	ProjectID   string
	ArchivedAt  time.Time
}
