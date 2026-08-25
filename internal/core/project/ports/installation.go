package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

// InstallationResult is the durable outcome of one install/uninstall command:
// the installation projection as the command's response presented it plus the
// project revision that response carried.
type InstallationResult struct {
	Installation    domain.Installation
	ProjectRevision int64
}

// StoredInstallationRequest is the persisted result of one consumed
// installation command key. ResultUninstalledAt snapshots the response's
// tombstone field so replays return the first result even after a later
// command changed the installation row.
type StoredInstallationRequest struct {
	Command             string
	RequestDigest       string
	InstallationID      string
	ProjectRevision     int64
	ResultUninstalledAt *time.Time
}

// InstallCommand is one fully validated install command. The application has
// already resolved the pinned registry version through the neutral catalog
// port and computed the canonical request digest; the repository executes
// installation, projection, revision, event, outbox, and idempotency mapping
// in one transaction.
type InstallCommand struct {
	OwnerUserID       string
	IdempotencyKey    string
	ProjectID         string
	AppID             string
	Pinned            domain.PinnedApp
	ExpectedRevision  int64
	RequestDigest     string
	NewInstallationID string
	Now               time.Time
}

// UninstallCommand is one fully validated uninstall command.
type UninstallCommand struct {
	OwnerUserID      string
	IdempotencyKey   string
	ProjectID        string
	InstallationID   string
	ExpectedRevision int64
	RequestDigest    string
	Now              time.Time
}

// InstallationRepository owns the installation facts. Concurrent commands are
// arbitrated by the database: the project row lock serializes mutations
// against every other Project revision writer, and the request-mapping
// primary key decides same-key races across projects.
type InstallationRepository interface {
	// LookupInstallationRequest returns the stored result when the key was
	// consumed, without touching any other state.
	LookupInstallationRequest(ctx context.Context, ownerUserID, idempotencyKey string) (StoredInstallationRequest, bool, error)
	// GetInstallation reads one installation by owner-scoped ID; it is the
	// replay projection source for consumed keys.
	GetInstallation(ctx context.Context, ownerUserID, installationID string) (domain.Installation, error)
	Install(ctx context.Context, command InstallCommand) (InstallationResult, error)
	Uninstall(ctx context.Context, command UninstallCommand) (InstallationResult, error)
	// ListActive returns at most limit active installations ordered by app ID
	// after the cursor; a missing, foreign, or archived project is NotFound.
	ListActive(ctx context.Context, ownerUserID, projectID, cursor string, limit int) ([]domain.Installation, error)
}
