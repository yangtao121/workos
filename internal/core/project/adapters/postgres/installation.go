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
	"github.com/yangtao121/workos/internal/platform/dbtransient"
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
		return ports.StoredInstallationRequest{}, false, fmt.Errorf("query installation request: %w", err)
	}
	return ports.StoredInstallationRequest{
		Command: stored.Command, RequestDigest: stored.RequestDigest,
		InstallationID: stored.InstallationID, ProjectRevision: stored.ProjectRevision,
		ResultUninstalledAt: timePtr(stored.ResultUninstalledAt),
	}, true, nil
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
	return installationFromDB(value, err)
}

// Install executes one install command in a single transaction: lock and
// classify the project, create or no-op the installation, bump the revision
// and refresh the installed_app_ids projection for real changes, append the
// project event and outbox row, and consume the idempotency key — or roll
// everything back.
func (r *Repository) Install(ctx context.Context, command ports.InstallCommand) (ports.InstallationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, fmt.Errorf("begin install app: %w", err)
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
			// Deterministic no-op under the lock: the expected revision was
			// verified, so the key is consumed against the existing fact —
			// without a second row, revision bump, or event.
			existing, err := installationFromDB(active, nil)
			if err != nil {
				return ports.InstallationResult{}, err
			}
			return r.commitInstallationRequest(ctx, tx, queries, projectdb.InsertInstallationRequestParams{
				OwnerUserID: command.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
				Command: "install", RequestDigest: command.RequestDigest, InstallationID: existing.ID,
				ProjectRevision: command.ExpectedRevision, CreatedAt: timestamp(command.Now),
			}, ports.InstallationResult{Installation: existing, ProjectRevision: command.ExpectedRevision})
		}
		return ports.InstallationResult{}, domain.ErrAlreadyInstalled
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.InstallationResult{}, fmt.Errorf("query active installation: %w", err)
	}

	installation := domain.Installation{
		ID: command.NewInstallationID, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		AppID: command.AppID, Version: command.Pinned.Version, ManifestDigest: command.Pinned.ManifestDigest,
		InstalledAt: command.Now,
	}
	if err := queries.InsertInstallation(ctx, projectdb.InsertInstallationParams{
		ID: installation.ID, OwnerUserID: installation.OwnerUserID, ProjectID: installation.ProjectID,
		AppID: installation.AppID, Version: installation.Version, ManifestDigest: installation.ManifestDigest,
		InstalledAt: timestamp(installation.InstalledAt),
	}); err != nil {
		return ports.InstallationResult{}, fmt.Errorf("insert installation: %w", err)
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
		ProjectRevision: projection.Revision, CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
}

// Uninstall tombstones one active installation in a single transaction with
// the same revision/projection/event/outbox/idempotency guarantees.
func (r *Repository) Uninstall(ctx context.Context, command ports.UninstallCommand) (ports.InstallationResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.InstallationResult{}, fmt.Errorf("begin uninstall app: %w", err)
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
		return ports.InstallationResult{}, fmt.Errorf("tombstone installation: %w", err)
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
		CreatedAt: timestamp(command.Now),
	}, ports.InstallationResult{Installation: installation, ProjectRevision: projection.Revision})
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
	values, err := r.queries.ListActiveInstallations(ctx, projectdb.ListActiveInstallationsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, Cursor: cursor, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list active installations", err)
	}
	installations := make([]domain.Installation, 0, len(values))
	for _, value := range values {
		installation, err := installationFromDB(value, nil)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	return installations, nil
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
		return ports.InstallationResult{}, true, fmt.Errorf("lock project for installation: %w", err)
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
		installation.UninstalledAt = timePtr(stored.ResultUninstalledAt)
		return ports.InstallationResult{Installation: installation, ProjectRevision: stored.ProjectRevision}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.InstallationResult{}, true, fmt.Errorf("query installation request under lock: %w", err)
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
		return projectdb.ApplyInstallationProjectionRow{}, fmt.Errorf("collect active app ids: %w", err)
	}
	projection, err := queries.ApplyInstallationProjection(ctx, projectdb.ApplyInstallationProjectionParams{
		UpdatedAt: timestamp(now), InstalledAppIds: appIDs,
		ID: projectID, OwnerUserID: ownerUserID, ExpectedRevision: expectedRevision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projectdb.ApplyInstallationProjectionRow{}, domain.ErrConflict
	}
	if err != nil {
		return projectdb.ApplyInstallationProjectionRow{}, fmt.Errorf("apply installation projection: %w", err)
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
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate installation event id: %w", err)
	}
	if err := queries.InsertProjectEvent(ctx, projectdb.InsertProjectEventParams{
		ID: eventID.String(), StreamID: installation.ProjectID, Sequence: revision, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(occurredAt),
	}); err != nil {
		return fmt.Errorf("append installation event: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate installation outbox id: %w", err)
	}
	if err := queries.InsertProjectOutbox(ctx, projectdb.InsertProjectOutboxParams{
		ID: outboxID.String(), AggregateID: installation.ProjectID, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(occurredAt),
	}); err != nil {
		return fmt.Errorf("append installation outbox: %w", err)
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
		return ports.InstallationResult{}, fmt.Errorf("insert installation request: %w", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return ports.InstallationResult{}, fmt.Errorf("commit installation command: %w", err)
		}
		return onSuccess, nil
	}
	consumed, err := queries.GetInstallationRequest(ctx, projectdb.GetInstallationRequestParams{
		OwnerUserID: params.OwnerUserID, IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		return ports.InstallationResult{}, fmt.Errorf("classify consumed installation request: %w", err)
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
	installation.UninstalledAt = timePtr(consumed.ResultUninstalledAt)
	return ports.InstallationResult{Installation: installation, ProjectRevision: consumed.ProjectRevision}, nil
}

func installationFromDB(value projectdb.WorkosCoreProjectAppInstallation, err error) (domain.Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Installation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Installation{}, storeError("query installation", err)
	}
	installation := domain.Installation{
		ID: value.ID, OwnerUserID: value.OwnerUserID, ProjectID: value.ProjectID,
		AppID: value.AppID, Version: value.Version, ManifestDigest: value.ManifestDigest,
		InstalledAt: value.InstalledAt.Time,
	}
	installation.UninstalledAt = timePtr(value.UninstalledAt)
	return installation, nil
}

// storeError wraps a storage failure at the port boundary. Transient
// dependency failures (unreachable server, broken connection, resource
// exhaustion) carry the ErrStoreUnavailable sentinel so transports can
// answer a sanitized Unavailable; every other failure stays an opaque
// internal error — classification never reads SQLSTATE message text or
// constraint names.
func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := value.Time
	return &timestamp
}
