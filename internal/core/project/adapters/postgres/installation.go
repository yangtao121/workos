package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// Installation repository methods live on the same Repository as Project
// CRUD because both own the same workos_core project boundary: installation
// writes share the Project revision/event/outbox sequence, and the
// installed_app_ids projection is computed in the very transaction that
// changes the active installation set.
//
// Concurrency is arbitrated exclusively by the database. Every mutation
// locks the owner-scoped project row (SELECT … FOR UPDATE), which also
// serializes against the guarded UPDATEs of UpdateProject/ArchiveProject;
// same-key command races on different projects are decided by the
// request-mapping primary key; the active-installation unique index is the
// final guard that only one active row per (project, app) can ever exist.

// LookupInstallationRequest returns the persisted result of a consumed key.
func (r *Repository) LookupInstallationRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.StoredInstallationRequest, bool, error) {
	stored, err := r.queries.GetInstallationRequest(ctx, projectdb.GetInstallationRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.StoredInstallationRequest{}, false, nil
	}
	if err != nil {
		return ports.StoredInstallationRequest{}, false, storeError("query installation request", err)
	}
	result := storedInstallationRequest(stored)
	if err := validateStoredInstallationRequest(result); err != nil {
		return ports.StoredInstallationRequest{}, false, err
	}
	return result, true, nil
}

// storedInstallationRequest projects the persisted request row; the result
// snapshot columns are NOT NULL with history backfilled, so the grant, epoch,
// and pinned version identity are always the first response's authoritative
// facts.
func storedInstallationRequest(stored projectdb.GetInstallationRequestRow) ports.StoredInstallationRequest {
	return ports.StoredInstallationRequest{
		Command: stored.Command, RequestDigest: stored.RequestDigest,
		InstallationID: stored.InstallationID, ProjectRevision: stored.ProjectRevision,
		ResultUninstalledAt:      timePtr(stored.ResultUninstalledAt),
		ResultGrantedPermissions: stored.ResultGrantedPermissions,
		ResultGrantRevision:      stored.ResultGrantRevision,
		ResultVersion:            stored.ResultVersion,
		ResultManifestDigest:     stored.ResultManifestDigest,
		CreatedAt:                stored.CreatedAt.Time,
	}
}

// GetInstallation reads one installation by owner-scoped ID.
func (r *Repository) GetInstallation(ctx context.Context, ownerUserID, installationID string) (domain.Installation, error) {
	value, err := r.queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
		OwnerUserID: ownerUserID, ID: installationID,
	})
	return installationFromDB(value, err)
}

// ResolveActiveInstallation reads the active installation of an owner's
// non-archived project in one query; every miss maps to a sanitized NotFound.
func (r *Repository) ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (domain.Installation, error) {
	value, err := r.queries.ResolveActiveInstallation(ctx, projectdb.ResolveActiveInstallationParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, ID: installationID,
	})
	return installationFromResolver(value, err)
}

func (r *Repository) ResolveActiveInstallationForNotificationTx(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID, installationID string) (domain.Installation, error) {
	queries := r.queries.WithTx(tx)
	if _, err := queries.LockProjectForNotification(ctx, projectdb.LockProjectForNotificationParams{
		OwnerUserID: ownerUserID, ProjectID: projectID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	} else if err != nil {
		return domain.Installation{}, storeError("lock project for notification authorization", err)
	}
	value, err := queries.ResolveActiveInstallationForNotification(ctx, projectdb.ResolveActiveInstallationForNotificationParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, ID: installationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Installation{}, storeError("lock installation for notification authorization", err)
	}
	return installationFromColumns(
		value.ID, value.OwnerUserID, value.ProjectID, value.AppID, value.Version, value.ManifestDigest,
		value.GrantedPermissions, value.GrantRevision, value.InstalledAt, value.UninstalledAt)
}

// Install executes one install command in a single transaction: lock and
// classify the project, create or no-op the installation, bump the revision
// and refresh the installed_app_ids projection for real changes, append the
// project event and outbox row, and consume the idempotency key — or roll
// everything back.
func (r *Repository) Install(ctx context.Context, command ports.InstallCommand) (ports.InstallationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, storeError("begin install app", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	// Under the project lock the key is re-checked first: a concurrent
	// identical command that committed while this transaction waited must
	// replay, not conflict on the now-advanced revision.
	if result, handled, err := classifyUnderLock(ctx, queries, command.OwnerUserID, command.IdempotencyKey, command.ProjectID, command.ExpectedRevision, command.RequestDigest); handled {
		return result, err
	}

	active, err := queries.GetActiveInstallationByApp(ctx, projectdb.GetActiveInstallationByAppParams{
		ProjectID: command.ProjectID, AppID: command.AppID,
	})
	if err == nil {
		if active.Version == command.Pinned.Version && active.ManifestDigest == command.Pinned.ManifestDigest {
			existing, err := installationFromActiveByApp(active, nil)
			if err != nil {
				return ports.InstallationResult{}, err
			}
			if !equalGrants(existing.GrantedPermissions, command.GrantedPermissions) {
				// Same pinned version but a different grant must never
				// silently re-grant: an explicit Set command is the only
				// grant-change path.
				return ports.InstallationResult{}, domain.ErrAlreadyInstalled
			}
			// Deterministic no-op under the lock: the expected revision was
			// verified, so the key is consumed against the existing fact —
			// without a second row, revision bump, or event. The result
			// snapshot pins the first-response grant and its epoch (read from
			// the locked row, not a constant) so a later replay never returns
			// a mutated row.
			return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
				OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
				Command: "install", RequestDigest: command.RequestDigest, InstallationID: existing.ID,
				ProjectRevision:          command.ExpectedRevision,
				ResultGrantedPermissions: nonNilGranted(existing.GrantedPermissions),
				ResultGrantRevision:      existing.GrantRevision,
				ResultVersion:            existing.Version, ResultManifestDigest: existing.ManifestDigest,
				CreatedAt: timestamp(command.Now),
			}, ports.InstallationResult{Installation: existing, ProjectRevision: command.ExpectedRevision})
		}
		return ports.InstallationResult{}, domain.ErrAlreadyInstalled
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.InstallationResult{}, storeError("query active installation", err)
	}

	installation := domain.Installation{
		ID: command.NewInstallationID, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		AppID: command.AppID, Version: command.Pinned.Version, ManifestDigest: command.Pinned.ManifestDigest,
		GrantedPermissions: command.GrantedPermissions,
		GrantRevision:      installTimeGrantRevision,
		InstalledAt:        command.Now,
	}
	if err := queries.InsertInstallation(ctx, projectdb.InsertInstallationParams{
		ID: installation.ID, OwnerUserID: installation.OwnerUserID, ProjectID: installation.ProjectID,
		AppID: installation.AppID, Version: installation.Version, ManifestDigest: installation.ManifestDigest,
		GrantedPermissions: nonNilGranted(installation.GrantedPermissions),
		InstalledAt:        timestamp(installation.InstalledAt),
	}); err != nil {
		return ports.InstallationResult{}, storeError("insert installation", err)
	}
	// The install origin is the first snapshot of the bounded version
	// history (ADR-0012): every installation is born with a rollbackable
	// past recorded in the same transaction that creates it.
	if err := queries.InsertInstallationVersion(ctx, projectdb.InsertInstallationVersionParams{
		InstallationID: installation.ID, OwnerUserID: installation.OwnerUserID,
		Sequence: installOriginSequence, Version: installation.Version,
		ManifestDigest: installation.ManifestDigest, Source: domain.VersionSourceInstall,
		OccurredAt: timestamp(command.Now),
	}); err != nil {
		return ports.InstallationResult{}, storeError("insert install origin snapshot", err)
	}
	projection, err := applyProjection(ctx, queries, command.OwnerUserID, command.ProjectID, command.ExpectedRevision, command.Now)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if err := appendInstallationEvent(ctx, queries, installation, projection.Revision, "project.app.installed.v1", command.Now); err != nil {
		return ports.InstallationResult{}, err
	}
	return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		Command: "install", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
		ProjectRevision:          projection.Revision,
		ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
		ResultGrantRevision:      installTimeGrantRevision,
		ResultVersion:            installation.Version, ResultManifestDigest: installation.ManifestDigest,
		CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
}

// installTimeGrantRevision is the grant epoch a fresh install creates its
// installation at (the column default after the mutable-grants migration).
// It only ever describes a newly inserted row; every snapshot of an existing
// installation must read GrantRevision from the locked row instead, so a
// replay of an old key never confuses a later SetAppGrants epoch with 1.
const installTimeGrantRevision = 1

// installOriginSequence is the history sequence of a fresh installation's
// install snapshot: every installation starts its bounded history at 1.
const installOriginSequence = int64(1)

// Uninstall tombstones one active installation in a single transaction with
// the same revision/projection/event/outbox/idempotency guarantees.
func (r *Repository) Uninstall(ctx context.Context, command ports.UninstallCommand) (ports.InstallationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, storeError("begin uninstall app", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	if result, handled, err := classifyUnderLock(ctx, queries, command.OwnerUserID, command.IdempotencyKey, command.ProjectID, command.ExpectedRevision, command.RequestDigest); handled {
		return result, err
	}

	value, err := queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
		OwnerUserID: command.OwnerUserID, ID: command.InstallationID,
	})
	installation, err := installationFromDB(value, err)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if installation.ProjectID != command.ProjectID || installation.UninstalledAt != nil {
		// A foreign-project or already-tombstoned installation is
		// indistinguishable from an unknown one.
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	rows, err := queries.TombstoneInstallation(ctx, projectdb.TombstoneInstallationParams{
		OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		ID: installation.ID, UninstalledAt: timestamp(command.Now),
	})
	if err != nil {
		return ports.InstallationResult{}, storeError("tombstone installation", err)
	}
	if rows == 0 {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	tombstone := command.Now
	installation.UninstalledAt = &tombstone
	projection, err := applyProjection(ctx, queries, command.OwnerUserID, command.ProjectID, command.ExpectedRevision, command.Now)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if err := appendInstallationEvent(ctx, queries, installation, projection.Revision, "project.app.uninstalled.v1", command.Now); err != nil {
		return ports.InstallationResult{}, err
	}
	return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		Command: "uninstall", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
		ProjectRevision: projection.Revision, ResultUninstalledAt: timestamp(tombstone),
		ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
		// The uninstall response reports the grant and epoch as of this
		// transaction, read from the locked installation row — not a constant,
		// so a later replay returns the facts the first response carried.
		ResultGrantRevision: installation.GrantRevision,
		ResultVersion:       installation.Version, ResultManifestDigest: installation.ManifestDigest,
		CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
}

// errGrantInvariantCorrupt marks a stored grant snapshot or grant revision
// that violates the canonical invariants (sorted, duplicate-free, every entry
// inside the pinned version's requested set, revision >= 1). Any drift here is
// internal corruption: the command fails closed with a sanitized Internal and
// is never silently repaired by the user's update.
var errGrantInvariantCorrupt = errors.New("stored installation grant facts are inconsistent")

// errPinnedIdentityDrift marks a re-read installation whose pinned identity
// no longer equals the catalog-resolved facts. Registry versions are
// immutable, so drift is corruption — never an upgrade path.
var errPinnedIdentityDrift = errors.New("installation pinned identity drifted")

// SetAppGrants replaces one active installation's entire grant set in a
// single transaction (ADR-0003). Under the owner-scoped project lock it
// re-arbitrates the idempotency key and expected revision, re-reads the
// active installation, re-verifies the pinned identity and the stored-grant
// invariants, then either consumes the key against unchanged facts
// (deterministic no-op: no revision bump, no event) or atomically applies
// the new grant with grant_revision+1, Project revision+1, the
// project.app.grants.updated.v1 event, the outbox row, and the idempotency
// result snapshot.
func (r *Repository) SetAppGrants(ctx context.Context, command ports.SetAppGrantsCommand) (ports.InstallationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, storeError("begin set app grants", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	// Lock, replay/conflict, and expected-revision arbitration share the exact
	// classifyUnderLock path of install/uninstall, so grant mutation joins the
	// same expected_project_revision serialization domain.
	if result, handled, err := classifyUnderLock(ctx, queries, command.OwnerUserID, command.IdempotencyKey, command.ProjectID, command.ExpectedRevision, command.RequestDigest); handled {
		return result, err
	}

	// Re-read the installation under the lock: foreign project, unknown, or
	// already uninstalled are indistinguishable sanitized misses (no TOCTOU
	// against a concurrent uninstall that held the lock first).
	value, err := queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
		OwnerUserID: command.OwnerUserID, ID: command.InstallationID,
	})
	installation, err := installationFromDB(value, err)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if installation.ProjectID != command.ProjectID || installation.UninstalledAt != nil {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	// The pinned identity the application resolved must still equal the
	// installation row; registry versions are immutable, so drift is
	// corruption, never an upgrade path.
	if installation.AppID != command.Pinned.AppID ||
		installation.Version != command.Pinned.Version || installation.ManifestDigest != command.Pinned.ManifestDigest {
		return ports.InstallationResult{}, errPinnedIdentityDrift
	}
	if err := validateStoredGrantInvariant(installation, command.Pinned); err != nil {
		return ports.InstallationResult{}, err
	}

	// Deterministic no-op: the target set equals the current canonical grant.
	// The key is still consumed with a snapshot of the current facts; neither
	// revision, the event, the outbox, nor updated_at may move.
	if equalGrants(installation.GrantedPermissions, command.GrantedPermissions) {
		return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
			OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
			Command: "set-grants", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
			ProjectRevision:          command.ExpectedRevision,
			ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
			ResultGrantRevision:      installation.GrantRevision,
			ResultVersion:            installation.Version, ResultManifestDigest: installation.ManifestDigest,
			CreatedAt: timestamp(command.Now),
		}, ports.InstallationResult{Installation: installation, ProjectRevision: command.ExpectedRevision})
	}

	updated, err := queries.SetInstallationGrants(ctx, projectdb.SetInstallationGrantsParams{
		GrantedPermissions: nonNilGranted(command.GrantedPermissions),
		OwnerUserID:        command.OwnerUserID, ProjectID: command.ProjectID, ID: installation.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The WHERE clause still guards active rows; under the project lock
		// this is unreachable except for drift, so fail closed as a miss.
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	if err != nil {
		return ports.InstallationResult{}, storeError("set installation grants", err)
	}
	installation.GrantedPermissions = updated.GrantedPermissions
	installation.GrantRevision = updated.GrantRevision
	projection, err := applyProjection(ctx, queries, command.OwnerUserID, command.ProjectID, command.ExpectedRevision, command.Now)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if err := appendAppGrantsUpdatedEvent(ctx, queries, installation, projection.Revision, command.Now); err != nil {
		return ports.InstallationResult{}, err
	}
	return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		Command: "set-grants", RequestDigest: command.RequestDigest, InstallationID: installation.ID,
		ProjectRevision:          projection.Revision,
		ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
		ResultGrantRevision:      installation.GrantRevision,
		ResultVersion:            installation.Version, ResultManifestDigest: installation.ManifestDigest,
		CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
}

// validateStoredGrantInvariant checks the persisted grant facts under the
// lock: the epoch must be a positive integer and the grant list must be
// canonical (grammar-valid, sorted, duplicate-free) and a subset of the
// pinned version's requested permissions. Corruption is reported, never
// repaired by the incoming command.
func validateStoredGrantInvariant(installation domain.Installation, pinned domain.PinnedApp) error {
	if installation.GrantRevision < installTimeGrantRevision {
		return errGrantInvariantCorrupt
	}
	previous := ""
	for _, entry := range installation.GrantedPermissions {
		if !domain.ValidCapabilityID(entry) {
			return errGrantInvariantCorrupt
		}
		if previous != "" && entry <= previous {
			// Unsorted or duplicated: both violate the canonical form.
			return errGrantInvariantCorrupt
		}
		previous = entry
	}
	if _, err := domain.CanonicalInstallationGrant(installation.GrantedPermissions, pinned.Permissions); err != nil {
		return errGrantInvariantCorrupt
	}
	return nil
}

// ListActive returns active installations ordered by app ID after the
// cursor; missing, foreign, or archived projects are NotFound so the read
// path matches the mutation paths' fail-closed behavior.
func (r *Repository) ListActive(ctx context.Context, ownerUserID, projectID, cursor string, limit int) ([]domain.Installation, error) {
	project, err := r.queries.GetProject(ctx, projectdb.GetProjectParams{OwnerUserID: ownerUserID, ID: projectID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && project.ArchivedAt.Valid) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, storeError("query project for installations", err)
	}
	return installationsFromList(r.queries.ListActiveInstallations(ctx, projectdb.ListActiveInstallationsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, Cursor: cursor, RowLimit: int32(limit),
	}))
}

// classifyUnderLock locks the owner-scoped project row and resolves the
// states every mutation shares: unknown/foreign/archived project, consumed
// key (replay or conflict), and stale expected revision. handled reports
// that the command is fully decided; otherwise the row lock is held for the
// caller's remaining work.
func classifyUnderLock(
	ctx context.Context, queries *projectdb.Queries,
	ownerUserID, idempotencyKey, projectID string, expectedRevision int64, requestDigest string,
) (result ports.InstallationResult, handled bool, err error) {
	project, err := queries.LockProjectForInstallation(ctx, projectdb.LockProjectForInstallationParams{
		OwnerUserID: ownerUserID, ID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && project.ArchivedAt.Valid) {
		// Archived projects fail closed exactly like unknown ones.
		return ports.InstallationResult{}, true, domain.ErrNotFound
	}
	if err != nil {
		return ports.InstallationResult{}, true, storeError("lock project for installation", err)
	}
	// Re-check the key under the lock: an identical concurrent command that
	// already committed replays instead of conflicting on the advanced
	// revision, while a different request for the same key conflicts.
	stored, err := queries.GetInstallationRequest(ctx, projectdb.GetInstallationRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		if stored.RequestDigest != requestDigest {
			return ports.InstallationResult{}, true, domain.ErrIdempotencyConflict
		}
		value, err := queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
			OwnerUserID: ownerUserID, ID: stored.InstallationID,
		})
		installation, err := installationFromDB(value, err)
		if err != nil {
			return ports.InstallationResult{}, true, err
		}
		installation, err = applyRequestSnapshot(installation, storedInstallationRequest(stored))
		if err != nil {
			return ports.InstallationResult{}, true, err
		}
		return ports.InstallationResult{Installation: installation, ProjectRevision: stored.ProjectRevision}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.InstallationResult{}, true, storeError("query installation request under lock", err)
	}
	if project.Revision != expectedRevision {
		return ports.InstallationResult{}, true, domain.ErrConflict
	}
	return ports.InstallationResult{}, false, nil
}

// applyProjection bumps the Project revision by exactly one and rewrites
// installed_app_ids from the active installation facts in the same
// transaction, so the array can never diverge from the authoritative rows.
func applyProjection(ctx context.Context, queries *projectdb.Queries, ownerUserID, projectID string, expectedRevision int64, now time.Time) (projectdb.ApplyInstallationProjectionRow, error) {
	appIDs, err := queries.ActiveInstallationAppIDs(ctx, projectID)
	if err != nil {
		return projectdb.ApplyInstallationProjectionRow{}, storeError("collect active app ids", err)
	}
	projection, err := queries.ApplyInstallationProjection(ctx, projectdb.ApplyInstallationProjectionParams{
		UpdatedAt: timestamp(now), InstalledAppIds: appIDs,
		ID: projectID, OwnerUserID: ownerUserID, ExpectedRevision: expectedRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projectdb.ApplyInstallationProjectionRow{}, domain.ErrConflict
	}
	if err != nil {
		return projectdb.ApplyInstallationProjectionRow{}, storeError("apply installation projection", err)
	}
	return projection, nil
}

// appendInstallationEvent appends the project stream event and outbox row
// with sequence equal to the new Project revision. The payload carries only
// stable identifiers plus the pinned registry reference.
func appendInstallationEvent(ctx context.Context, queries *projectdb.Queries, installation domain.Installation, revision int64, eventType string, occurredAt time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"projectId": installation.ProjectID, "revision": revision, "installationId": installation.ID,
		"appId": installation.AppID, "version": installation.Version, "manifestDigest": installation.ManifestDigest,
	})
	if err != nil {
		return fmt.Errorf("encode installation event: %w", err)
	}
	return appendProjectEventOutbox(ctx, queries, installation.ProjectID, eventType, revision, payload, occurredAt)
}

// appGrantsUpdatedEvent is the versioned event type of a real SetAppGrants
// change (ADR-0003); a same-set no-op emits nothing.
const appGrantsUpdatedEvent = "project.app.grants.updated.v1"

// appGrantsUpdatedPayload builds the event payload of one real grant change.
// It carries the complete canonical grant set (not an added/removed diff) so
// consumers can rebuild the current authorization fact without history, plus
// only stable, non-sensitive identifiers: no manifest, goal, task/event
// content, token, credential, or raw user content.
func appGrantsUpdatedPayload(installation domain.Installation, revision, grantRevision int64, granted []string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"projectId": installation.ProjectID, "revision": revision, "installationId": installation.ID,
		"appId": installation.AppID, "version": installation.Version, "manifestDigest": installation.ManifestDigest,
		"grantRevision": grantRevision, "grantedPermissions": granted,
	})
}

// appendAppGrantsUpdatedEvent appends the project.app.grants.updated.v1
// event and outbox row with sequence equal to the new Project revision.
func appendAppGrantsUpdatedEvent(ctx context.Context, queries *projectdb.Queries, installation domain.Installation, revision int64, occurredAt time.Time) error {
	payload, err := appGrantsUpdatedPayload(installation, revision, installation.GrantRevision, installation.GrantedPermissions)
	if err != nil {
		return fmt.Errorf("encode app grants event: %w", err)
	}
	return appendProjectEventOutbox(ctx, queries, installation.ProjectID, appGrantsUpdatedEvent, revision, payload, occurredAt)
}

// appendProjectEventOutbox writes one project stream event and one outbox
// row with UUIDv7 identifiers inside the caller's transaction; sequence is
// always the new Project revision.
func appendProjectEventOutbox(ctx context.Context, queries *projectdb.Queries, streamID, eventType string, sequence int64, payload []byte, occurredAt time.Time) error {
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate installation event id: %w", err)
	}
	if err := queries.InsertProjectEvent(ctx, projectdb.InsertProjectEventParams{
		ID: eventID.String(), StreamID: streamID, Sequence: sequence, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(occurredAt),
	}); err != nil {
		return storeError("append installation event", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate installation outbox id: %w", err)
	}
	if err := queries.InsertProjectOutbox(ctx, projectdb.InsertProjectOutboxParams{
		ID: outboxID.String(), AggregateID: streamID, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(occurredAt),
	}); err != nil {
		return storeError("append installation outbox", err)
	}
	return nil
}

// commitInstallationRequest atomically consumes the idempotency key with the
// command's result and commits. When a concurrent transaction consumed the
// same key first, the primary key leaves this insert at zero rows: the
// loser re-reads the mapping and replays the identical request or conflicts,
// and the rolled-back transaction leaves no orphan installation, revision,
// event, or outbox row.
func (r *Repository) commitInstallationRequest(
	ctx context.Context, tx pgx.Tx, queries *projectdb.Queries,
	params projectdb.InsertInstallationRequestParams, onSuccess ports.InstallationResult,
) (ports.InstallationResult, error) {
	rows, err := queries.InsertInstallationRequest(ctx, params)
	if err != nil {
		return ports.InstallationResult{}, storeError("insert installation request", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return ports.InstallationResult{}, storeError("commit installation command", err)
		}
		return onSuccess, nil
	}
	consumed, err := queries.GetInstallationRequest(ctx, projectdb.GetInstallationRequestParams{
		OwnerUserID: params.OwnerUserID, IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		return ports.InstallationResult{}, storeError("classify consumed installation request", err)
	}
	if consumed.RequestDigest != params.RequestDigest {
		return ports.InstallationResult{}, domain.ErrIdempotencyConflict
	}
	value, err := queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
		OwnerUserID: params.OwnerUserID, ID: consumed.InstallationID,
	})
	installation, err := installationFromDB(value, err)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	installation, err = applyRequestSnapshot(installation, storedInstallationRequest(consumed))
	if err != nil {
		return ports.InstallationResult{}, err
	}
	return ports.InstallationResult{Installation: installation, ProjectRevision: consumed.ProjectRevision}, nil
}

// applyRequestSnapshot overlays the persisted first-response snapshot onto
// the current installation row so a replay returns the facts the first
// command returned — tombstone, grant set, grant epoch, and the pinned
// version identity — even after a later SetAppGrants, uninstall, or version
// transition mutated the row.
func applyRequestSnapshot(installation domain.Installation, stored ports.StoredInstallationRequest) (domain.Installation, error) {
	if err := validateStoredInstallationRequest(stored); err != nil {
		return domain.Installation{}, err
	}
	installation.UninstalledAt = stored.ResultUninstalledAt
	installation.GrantedPermissions = stored.ResultGrantedPermissions
	installation.GrantRevision = stored.ResultGrantRevision
	installation.Version = stored.ResultVersion
	installation.ManifestDigest = stored.ResultManifestDigest
	if err := domain.ValidateStoredInstallation(installation); err != nil {
		return domain.Installation{}, err
	}
	return installation, nil
}

func validateStoredInstallationRequest(stored ports.StoredInstallationRequest) error {
	switch stored.Command {
	case "install", "uninstall", "set-grants", domain.VersionSourceTransition, domain.VersionSourceRollback:
	default:
		return domain.ErrInstallationCorrupt
	}
	if !domain.ValidInstallationManifestDigest(stored.RequestDigest) ||
		!domain.ValidStoredInstallationUUID(stored.InstallationID) ||
		stored.ProjectRevision < 1 || stored.ResultGrantRevision < 1 ||
		!domain.ValidInstallationVersion(stored.ResultVersion) ||
		!domain.ValidInstallationManifestDigest(stored.ResultManifestDigest) ||
		!domain.ValidStoredInstallationTime(stored.CreatedAt) {
		return domain.ErrInstallationCorrupt
	}
	previous := ""
	for _, capability := range stored.ResultGrantedPermissions {
		if !domain.ValidCapabilityID(capability) || (previous != "" && capability <= previous) {
			return domain.ErrInstallationCorrupt
		}
		previous = capability
	}
	if stored.ResultUninstalledAt != nil && !domain.ValidStoredInstallationTime(*stored.ResultUninstalledAt) {
		return domain.ErrInstallationCorrupt
	}
	return nil
}

func installationFromDB(value projectdb.GetInstallationByIdRow, err error) (domain.Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Installation{}, storeError("query installation", err)
	}
	return installationFromColumns(
		value.ID, value.OwnerUserID, value.ProjectID, value.AppID, value.Version, value.ManifestDigest,
		value.GrantedPermissions, value.GrantRevision, value.InstalledAt, value.UninstalledAt)
}

func installationFromActiveByApp(value projectdb.GetActiveInstallationByAppRow, err error) (domain.Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Installation{}, storeError("query installation", err)
	}
	return installationFromColumns(
		value.ID, value.OwnerUserID, value.ProjectID, value.AppID, value.Version, value.ManifestDigest,
		value.GrantedPermissions, value.GrantRevision, value.InstalledAt, value.UninstalledAt)
}

func installationFromResolver(value projectdb.ResolveActiveInstallationRow, err error) (domain.Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Installation{}, storeError("query installation", err)
	}
	return installationFromColumns(
		value.ID, value.OwnerUserID, value.ProjectID, value.AppID, value.Version, value.ManifestDigest,
		value.GrantedPermissions, value.GrantRevision, value.InstalledAt, value.UninstalledAt)
}

func installationsFromList(values []projectdb.ListActiveInstallationsRow, err error) ([]domain.Installation, error) {
	if err != nil {
		return nil, storeError("list installations", err)
	}
	installations := make([]domain.Installation, 0, len(values))
	for _, value := range values {
		installation, err := installationFromColumns(
			value.ID, value.OwnerUserID, value.ProjectID, value.AppID, value.Version, value.ManifestDigest,
			value.GrantedPermissions, value.GrantRevision, value.InstalledAt, value.UninstalledAt)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	return installations, nil
}

func installationFromColumns(
	id, ownerUserID, projectID, appID, version, manifestDigest string, grantedPermissions []string, grantRevision int64,
	installedAt, uninstalledAt pgtype.Timestamptz,
) (domain.Installation, error) {
	installation := domain.Installation{
		ID: id, OwnerUserID: ownerUserID, ProjectID: projectID,
		AppID: appID, Version: version, ManifestDigest: manifestDigest,
		GrantedPermissions: grantedPermissions,
		GrantRevision:      grantRevision,
		InstalledAt:        installedAt.Time,
	}
	installation.UninstalledAt = timePtr(uninstalledAt)
	if err := domain.ValidateStoredInstallation(installation); err != nil {
		return domain.Installation{}, err
	}
	return installation, nil
}

// nonNilGranted maps a nil slice to the empty grant so the NOT NULL array
// column never receives SQL NULL.
func nonNilGranted(granted []string) []string {
	if granted == nil {
		return []string{}
	}
	return granted
}

// equalGrants compares two canonical (sorted, duplicate-free) grant sets.
func equalGrants(stored, requested []string) bool {
	if len(stored) != len(requested) {
		return false
	}
	for index := range stored {
		if stored[index] != requested[index] {
			return false
		}
	}
	return true
}

// errHistoryCorrupt marks stored version-history rows that violate the
// canonical invariants or the owner binding. Corruption is sanitized
// Internal, never a silent repair.
var errHistoryCorrupt = errors.New("stored installation version history is inconsistent")

// versionUpdatedEvent is the versioned event type of a real version change
// (ADR-0012); a same-version transition no-op emits nothing.
const versionUpdatedEvent = "project.app.version.updated.v1"

// Transition pins the command's exact target registry version onto the active
// installation in a single transaction (ADR-0012). Under the owner-scoped
// project lock it re-arbitrates the idempotency key and expected revision,
// re-reads the active installation, re-checks grant compatibility against the
// target's requested permissions (a concurrent SetAppGrants may have moved
// the grants while this command waited for the lock), and — for rollback —
// re-derives the target from the durable history so a candidate selected
// before the lock can never pin an unverified version. A transition to the
// current (version, digest) is a deterministic no-op that still consumes the
// key; a real change appends the history snapshot (trimmed to the bounded
// limit), bumps the Project revision by exactly one, and commits the
// installation update, project event, outbox, and idempotency result
// atomically.
func (r *Repository) Transition(ctx context.Context, command ports.TransitionCommand) (ports.InstallationResult, error) {
	if command.Source != domain.VersionSourceTransition && command.Source != domain.VersionSourceRollback {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	if !domain.ValidInstallationVersion(command.Target.Version) ||
		!domain.ValidInstallationManifestDigest(command.Target.ManifestDigest) {
		return ports.InstallationResult{}, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, storeError("begin transition app version", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	if result, handled, err := classifyUnderLock(ctx, queries, command.OwnerUserID, command.IdempotencyKey, command.ProjectID, command.ExpectedRevision, command.RequestDigest); handled {
		return result, err
	}

	value, err := queries.GetInstallationById(ctx, projectdb.GetInstallationByIdParams{
		OwnerUserID: command.OwnerUserID, ID: command.InstallationID,
	})
	installation, err := installationFromDB(value, err)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if installation.ProjectID != command.ProjectID || installation.UninstalledAt != nil {
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	// Re-check grant compatibility under the lock: the application validated
	// against the pre-lock grants, and a concurrent SetAppGrants that
	// committed while this command waited must not be silently overridden by
	// a version change.
	if err := domain.GrantsCompatibleWithTarget(installation.GrantedPermissions, command.Target.Permissions); err != nil {
		return ports.InstallationResult{}, err
	}

	if command.Source == domain.VersionSourceRollback {
		history, err := installationHistory(ctx, queries, command.OwnerUserID, command.InstallationID)
		if err != nil {
			return ports.InstallationResult{}, err
		}
		if err := domain.ValidateVersionHistoryForInstallation(history, installation); err != nil {
			return ports.InstallationResult{}, err
		}
		candidate, err := deriveRollbackCandidate(history, installation)
		if err != nil {
			return ports.InstallationResult{}, err
		}
		if candidate.Version != command.Target.Version || candidate.ManifestDigest != command.Target.ManifestDigest {
			// The installation or its history moved between the
			// application's read and this lock: a stable conflict the
			// client resolves by re-reading, never a pinned mismatch.
			return ports.InstallationResult{}, domain.ErrConflict
		}
	}

	// Deterministic no-op: the transition target equals the current pinned
	// identity. The key is still consumed with a snapshot of the current
	// facts; neither revision, the event, the outbox, the history, nor
	// updated_at move. A rollback can never take this path — its target
	// differs from the current identity by derivation.
	if installation.Version == command.Target.Version && installation.ManifestDigest == command.Target.ManifestDigest {
		return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
			OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
			Command: command.Source, RequestDigest: command.RequestDigest, InstallationID: installation.ID,
			ProjectRevision:          command.ExpectedRevision,
			ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
			ResultGrantRevision:      installation.GrantRevision,
			ResultVersion:            installation.Version, ResultManifestDigest: installation.ManifestDigest,
			CreatedAt: timestamp(command.Now),
		}, ports.InstallationResult{Installation: installation, ProjectRevision: command.ExpectedRevision})
	}

	sequence, err := queries.NextInstallationVersionSequence(ctx, command.InstallationID)
	if err != nil {
		return ports.InstallationResult{}, storeError("next version sequence", err)
	}
	if err := queries.InsertInstallationVersion(ctx, projectdb.InsertInstallationVersionParams{
		InstallationID: command.InstallationID, OwnerUserID: command.OwnerUserID,
		Sequence: int64(sequence), Version: command.Target.Version,
		ManifestDigest: command.Target.ManifestDigest, Source: command.Source,
		OccurredAt: timestamp(command.Now),
	}); err != nil {
		return ports.InstallationResult{}, storeError("insert version snapshot", err)
	}
	rows, err := queries.UpdateInstallationVersion(ctx, projectdb.UpdateInstallationVersionParams{
		OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		ID: command.InstallationID, Version: command.Target.Version,
		ManifestDigest: command.Target.ManifestDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) || rows == 0 {
		// The guarded WHERE still excludes tombstoned rows; under the lock
		// this is unreachable except for drift, so fail closed as a miss.
		return ports.InstallationResult{}, domain.ErrNotFound
	}
	if err != nil {
		return ports.InstallationResult{}, storeError("update installation version", err)
	}
	fromVersion := installation.Version
	installation.Version = command.Target.Version
	installation.ManifestDigest = command.Target.ManifestDigest
	projection, err := applyProjection(ctx, queries, command.OwnerUserID, command.ProjectID, command.ExpectedRevision, command.Now)
	if err != nil {
		return ports.InstallationResult{}, err
	}
	if err := appendVersionUpdatedEvent(ctx, queries, installation, fromVersion, command.Source, projection.Revision, command.Now); err != nil {
		return ports.InstallationResult{}, err
	}
	if err := queries.TrimInstallationVersions(ctx, projectdb.TrimInstallationVersionsParams{
		InstallationID: command.InstallationID, Sequence: domain.VersionHistoryLimit,
	}); err != nil {
		return ports.InstallationResult{}, storeError("trim version history", err)
	}
	return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
		OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		Command: command.Source, RequestDigest: command.RequestDigest, InstallationID: installation.ID,
		ProjectRevision:          projection.Revision,
		ResultGrantedPermissions: nonNilGranted(installation.GrantedPermissions),
		ResultGrantRevision:      installation.GrantRevision,
		ResultVersion:            installation.Version, ResultManifestDigest: installation.ManifestDigest,
		CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
}

// deriveRollbackCandidate selects the most recent history snapshot whose
// (version, digest) differs from the installation's current pinned identity.
func deriveRollbackCandidate(history []domain.VersionSnapshot, installation domain.Installation) (domain.VersionSnapshot, error) {
	for index := len(history) - 1; index >= 0; index-- {
		snapshot := history[index]
		if snapshot.Version != installation.Version || snapshot.ManifestDigest != installation.ManifestDigest {
			return snapshot, nil
		}
	}
	return domain.VersionSnapshot{}, domain.ErrNoPreviousVersion
}

// ListAllVersions returns the installation's full version history oldest
// first. Every row's owner binding is re-verified; an installation without
// any history row is stored corruption (migration 025 seeds every existing
// installation and every install appends its origin snapshot atomically).
func (r *Repository) ListAllVersions(ctx context.Context, ownerUserID, installationID string) ([]domain.VersionSnapshot, error) {
	rows, err := r.queries.ListInstallationVersionsAsc(ctx, installationID)
	if err != nil {
		return nil, storeError("list installation versions", err)
	}
	if len(rows) == 0 {
		return nil, errHistoryCorrupt
	}
	snapshots := make([]domain.VersionSnapshot, 0, len(rows))
	for _, row := range rows {
		if row.OwnerUserID != ownerUserID ||
			!domain.ValidStoredInstallationUUID(row.InstallationID) ||
			!domain.ValidStoredInstallationUUID(row.OwnerUserID) {
			return nil, errHistoryCorrupt
		}
		snapshots = append(snapshots, domain.VersionSnapshot{
			Version: row.Version, ManifestDigest: row.ManifestDigest,
			Source: row.Source, Sequence: row.Sequence, OccurredAt: row.OccurredAt.Time,
		})
	}
	return snapshots, nil
}

// appendVersionUpdatedEvent appends the project.app.version.updated.v1 event
// and outbox row with sequence equal to the new Project revision. The
// payload carries only stable identifiers and the version facts — no
// manifest content, credentials, or user content.
func appendVersionUpdatedEvent(ctx context.Context, queries *projectdb.Queries, installation domain.Installation, fromVersion, source string, revision int64, occurredAt time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"projectId": installation.ProjectID, "revision": revision, "installationId": installation.ID,
		"appId": installation.AppID, "fromVersion": fromVersion, "toVersion": installation.Version,
		"manifestDigest": installation.ManifestDigest, "source": source,
	})
	if err != nil {
		return fmt.Errorf("encode version event: %w", err)
	}
	return appendProjectEventOutbox(ctx, queries, installation.ProjectID, versionUpdatedEvent, revision, payload, occurredAt)
}

// installationHistory reads and owner-verifies the history rows inside the
// caller's transaction.
func installationHistory(ctx context.Context, queries *projectdb.Queries, ownerUserID, installationID string) ([]domain.VersionSnapshot, error) {
	rows, err := queries.ListInstallationVersionsAsc(ctx, installationID)
	if err != nil {
		return nil, storeError("list installation versions", err)
	}
	if len(rows) == 0 {
		return nil, errHistoryCorrupt
	}
	snapshots := make([]domain.VersionSnapshot, 0, len(rows))
	for _, row := range rows {
		if row.OwnerUserID != ownerUserID ||
			!domain.ValidStoredInstallationUUID(row.InstallationID) ||
			!domain.ValidStoredInstallationUUID(row.OwnerUserID) {
			return nil, errHistoryCorrupt
		}
		snapshots = append(snapshots, domain.VersionSnapshot{
			Version: row.Version, ManifestDigest: row.ManifestDigest,
			Source: row.Source, Sequence: row.Sequence, OccurredAt: row.OccurredAt.Time,
		})
	}
	return snapshots, nil
}
