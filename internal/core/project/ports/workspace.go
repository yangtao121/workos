package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

// WorkspaceSource is one operator-registered directory reported by a runtime
// host. It carries bounded descriptive facts only: never a host path.
type WorkspaceSource struct {
	ID          string
	Kind        string
	DisplayName string
	ReadOnly    bool
	Registered  time.Time
}

// SourceDirectory reads the operator-registered workspace sources of one
// owner from the runtime host. Core never trusts client-submitted paths.
type SourceDirectory interface {
	Sources(ctx context.Context, ownerUserID string) ([]WorkspaceSource, error)
}

// WorkspaceRepository persists Core-owned workspace binding facts.
type WorkspaceRepository interface {
	// InsertWorkspaceBinding creates the binding or reports the consumed
	// key's stored digest for replay/conflict adjudication.
	InsertWorkspaceBinding(ctx context.Context, binding domain.WorkspaceBinding, requestDigest string) (storedDigest string, created bool, err error)
	GetWorkspaceBinding(ctx context.Context, ownerUserID, bindingID string) (domain.WorkspaceBinding, error)
	GetWorkspaceBindingByIdempotency(ctx context.Context, ownerUserID, idempotencyKey string) (domain.WorkspaceBinding, error)
	GetActiveWorkspaceBindingForProject(ctx context.Context, ownerUserID, projectID string) (domain.WorkspaceBinding, error)
	ListWorkspaceBindings(ctx context.Context, ownerUserID, projectID string, includeArchived bool) ([]domain.WorkspaceBinding, error)
	UpdateWorkspaceAccess(ctx context.Context, ownerUserID, bindingID string, readOnly bool, expectedRevision int64, now time.Time) (domain.WorkspaceBinding, error)
	ArchiveWorkspaceBinding(ctx context.Context, ownerUserID, bindingID string, expectedRevision int64, now time.Time) (domain.WorkspaceBinding, error)
}
