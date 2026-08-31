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
// installation command key. The result snapshot columns pin the response's
// tombstone field, grant set, grant epoch, and — since ADR-0012 — the exact
// pinned version identity, so replays return the first result even after a
// later SetAppGrants, uninstall, or version transition changed the row.
type StoredInstallationRequest struct {
	Command             string
	RequestDigest       string
	InstallationID      string
	ProjectRevision     int64
	ResultUninstalledAt *time.Time
	// ResultGrantedPermissions and ResultGrantRevision snapshot the grant
	// facts the first response carried; the result columns are NOT NULL with
	// history backfilled, so both are always authoritative.
	ResultGrantedPermissions []string
	ResultGrantRevision      int64
	// ResultVersion and ResultManifestDigest snapshot the pinned identity the
	// first response carried (NOT NULL since migration 025's fail-closed
	// backfill; identity was immutable before that migration, so the backfill
	// is exact for every pre-025 row).
	ResultVersion        string
	ResultManifestDigest string
}

// InstallCommand is one fully validated install command. The application has
// already resolved the pinned registry version through the neutral catalog
// port, canonicalized the grant snapshot against the pinned version's
// requested permissions, and computed the canonical request digest; the
// repository executes installation, projection, revision, event, outbox, and
// idempotency mapping in one transaction.
type InstallCommand struct {
	OwnerUserID    string
	IdempotencyKey string
	ProjectID      string
	AppID          string
	Pinned         domain.PinnedApp
	// GrantedPermissions is the canonical sorted grant snapshot to persist;
	// it is already a validated subset of Pinned.Permissions.
	GrantedPermissions []string
	ExpectedRevision   int64
	RequestDigest      string
	NewInstallationID  string
	Now                time.Time
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

// SetAppGrantsCommand is one fully validated full-replacement grant command
// (ADR-0003). The application has already canonicalized the target grant,
// resolved the exact pinned registry version through the neutral catalog port,
// checked the target against that version's requested permissions, and
// computed the canonical request digest; the repository re-verifies pinned
// identity and stored invariants under the project lock and executes the
// deterministic no-op or the atomic grant/revision/event/outbox/idempotency
// mutation in one transaction.
type SetAppGrantsCommand struct {
	OwnerUserID    string
	IdempotencyKey string
	ProjectID      string
	InstallationID string
	// Pinned is the exact registry version resolved from the installation's
	// pinned facts; the repository re-checks identity equality under the lock
	// and uses its requested permissions for the stored-subset invariant.
	Pinned domain.PinnedApp
	// GrantedPermissions is the canonical sorted target set; it is already a
	// validated subset of Pinned.Permissions. Empty means revoke all.
	GrantedPermissions []string
	ExpectedRevision   int64
	RequestDigest      string
	Now                time.Time
}

// TransitionCommand is one fully validated version command (ADR-0012): the
// explicit transition and the server-derived rollback share this path. The
// application has already resolved the exact target registry version through
// the neutral catalog port, verified grant compatibility, and computed the
// canonical request digest; the repository re-arbitrates idempotency and the
// expected revision under the project lock, re-derives the rollback target
// from the durable history inside the transaction, and commits the
// installation update, history append (with trim), project revision, event,
// outbox, and idempotency result atomically.
type TransitionCommand struct {
	OwnerUserID    string
	IdempotencyKey string
	ProjectID      string
	InstallationID string
	// Target is the exact registry version being pinned (transition: the
	// client-named version; rollback: the candidate Core derived from the
	// history outside the transaction). For rollback the repository
	// re-derives the target from the history under the lock and fails with
	// ErrConflict if the candidate no longer matches.
	Target           domain.PinnedApp
	Source           string // domain.VersionSourceTransition | domain.VersionSourceRollback
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
	// ResolveActiveInstallation reads one active installation of the owner's
	// non-archived project; anything else is NotFound. It is the authority for
	// installed-instance surface resolution.
	ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (domain.Installation, error)
	Install(ctx context.Context, command InstallCommand) (InstallationResult, error)
	Uninstall(ctx context.Context, command UninstallCommand) (InstallationResult, error)
	// SetAppGrants replaces one active installation's entire grant set in one
	// transaction. A target equal to the current canonical grant is a
	// deterministic no-op that still consumes the key; a real change bumps the
	// installation grant revision and the Project revision by exactly one and
	// commits the grant update, project event, outbox, and idempotency result
	// atomically. Stored-grant or revision invariant corruption is a sanitized
	// Internal, never a silent repair.
	SetAppGrants(ctx context.Context, command SetAppGrantsCommand) (InstallationResult, error)
	// Transition pins the command's exact target registry version onto the
	// active installation in one transaction (ADR-0012). A target equal to
	// the current (version, digest) is a deterministic no-op that still
	// consumes the key; a real change appends the history snapshot (trimmed
	// to the bounded limit), bumps the Project revision by exactly one, and
	// commits the installation update, project event, outbox, and idempotency
	// result atomically. For rollback the repository re-derives the target
	// from the durable history under the lock and rejects a stale candidate
	// with ErrConflict.
	Transition(ctx context.Context, command TransitionCommand) (InstallationResult, error)
	// ListAllVersions returns the installation's full version history oldest
	// first. It is the authority read for rollback candidate selection and
	// the public history projection.
	ListAllVersions(ctx context.Context, ownerUserID, installationID string) ([]domain.VersionSnapshot, error)
	// ListActive returns at most limit active installations ordered by app ID
	// after the cursor; a missing, foreign, or archived project is NotFound.
	ListActive(ctx context.Context, ownerUserID, projectID, cursor string, limit int) ([]domain.Installation, error)
}
