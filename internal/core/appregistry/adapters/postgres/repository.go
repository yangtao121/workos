package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres/appdb"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

// Repository stores App versions in Core-owned PostgreSQL tables. Concurrency
// is decided by table constraints inside one transaction, never by process
// state or a read-then-write race: the registration-request mapping is the
// single idempotency authority, so a key is only consumed on the paths that
// return success, and every success consumes it exactly once.
type Repository struct {
	pool    *pgxpool.Pool
	queries *appdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: appdb.New(pool)}
}

func (r *Repository) Register(ctx context.Context, record domain.AppVersion) (domain.AppVersionSummary, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.AppVersionSummary{}, fmt.Errorf("begin register app version: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	// An already-consumed key rules first: same normalized request replays
	// the stored fact, any different request conflicts, and neither outcome
	// may be bypassed by the state of the target version.
	request, err := queries.GetRegistrationRequest(ctx, appdb.GetRegistrationRequestParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
	})
	if err == nil {
		if request.RequestDigest != record.RequestDigest {
			return domain.AppVersionSummary{}, domain.ErrIdempotencyConflict
		}
		version, err := queries.GetAppVersionByID(ctx, request.AppVersionID)
		if err != nil {
			return domain.AppVersionSummary{}, fmt.Errorf("query idempotent app version: %w", err)
		}
		return summaryFromDB(version), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AppVersionSummary{}, fmt.Errorf("query registration request: %w", err)
	}

	// The immutable version insert is arbitrated by the unique constraint.
	rows, err := queries.InsertAppVersion(ctx, appdb.InsertAppVersionParams{
		ID: record.ID, OwnerUserID: record.OwnerUserID, AppID: record.AppID, Version: record.Version,
		Scope: string(record.Scope), Name: record.Name, Permissions: record.Permissions,
		ManifestDigest: record.ManifestDigest, CanonicalManifest: record.CanonicalManifest,
		CreatedAt: timestamp(record.CreatedAt),
	})
	if err != nil {
		return domain.AppVersionSummary{}, fmt.Errorf("insert app version: %w", err)
	}
	if rows == 0 {
		existing, err := queries.GetAppVersion(ctx, appdb.GetAppVersionParams{
			OwnerUserID: record.OwnerUserID, AppID: record.AppID, Version: record.Version,
		})
		if err != nil {
			return domain.AppVersionSummary{}, fmt.Errorf("query conflicting app version: %w", err)
		}
		if existing.ManifestDigest != record.ManifestDigest {
			// The version fact is immutable, but a key consumed concurrently by
			// a different request still dominates this verdict.
			if consumed, keyErr := queries.GetRegistrationRequest(ctx, appdb.GetRegistrationRequestParams{
				OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
			}); keyErr == nil && consumed.RequestDigest != record.RequestDigest {
				return domain.AppVersionSummary{}, domain.ErrIdempotencyConflict
			} else if keyErr != nil && !errors.Is(keyErr, pgx.ErrNoRows) {
				return domain.AppVersionSummary{}, fmt.Errorf("query conflicting registration request: %w", keyErr)
			}
			return domain.AppVersionSummary{}, domain.ErrVersionExists
		}
		// Same immutable fact under a new key: persist the mapping in the same
		// transaction so the success is replayable by this key later.
		return r.consumeKey(ctx, tx, queries, record, existing.ID, summaryFromDB(existing))
	}

	// Fresh version: consume the key atomically with the insert.
	return r.consumeKey(ctx, tx, queries, record, record.ID, domain.SummaryOf(record))
}

// consumeKey inserts the registration-request mapping and commits. If another
// transaction consumed the same key first, the loser re-reads the mapping:
// the identical normalized request replays the stored fact, anything else is
// a conflict, and the rolled-back loser leaves no orphan version or mapping.
func (r *Repository) consumeKey(
	ctx context.Context, tx pgx.Tx, queries *appdb.Queries,
	record domain.AppVersion, versionID string, onSuccess domain.AppVersionSummary,
) (domain.AppVersionSummary, error) {
	rows, err := queries.InsertRegistrationRequest(ctx, appdb.InsertRegistrationRequestParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
		RequestDigest: record.RequestDigest, AppVersionID: versionID, CreatedAt: timestamp(record.CreatedAt),
	})
	if err != nil {
		return domain.AppVersionSummary{}, fmt.Errorf("insert registration request: %w", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.AppVersionSummary{}, fmt.Errorf("commit register app version: %w", err)
		}
		return onSuccess, nil
	}
	consumed, err := queries.GetRegistrationRequest(ctx, appdb.GetRegistrationRequestParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
	})
	if err != nil {
		return domain.AppVersionSummary{}, fmt.Errorf("classify consumed registration request: %w", err)
	}
	if consumed.RequestDigest != record.RequestDigest {
		return domain.AppVersionSummary{}, domain.ErrIdempotencyConflict
	}
	version, err := queries.GetAppVersionByID(ctx, consumed.AppVersionID)
	if err != nil {
		return domain.AppVersionSummary{}, fmt.Errorf("query consumed app version: %w", err)
	}
	return summaryFromDB(version), nil
}

// summarySelect lists exactly the columns public projections and SemVer
// comparison need; read paths never select the canonical manifest.
const summarySelect = `
SELECT owner_user_id, app_id, version, scope, name, permissions, manifest_digest
FROM workos_core.app_versions`

func (r *Repository) GetVersion(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersionSummary, error) {
	rows, err := r.pool.Query(ctx, summarySelect+`
WHERE owner_user_id = $1 AND app_id = $2 AND version = $3
LIMIT 1`, ownerUserID, appID, version)
	if err != nil {
		return domain.AppVersionSummary{}, appVersionError("query app version", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.AppVersionSummary{}, appVersionError("query app version", err)
		}
		return domain.AppVersionSummary{}, domain.ErrNotFound
	}
	summary, err := scanSummary(rows)
	if err != nil {
		return domain.AppVersionSummary{}, err
	}
	return summary, nil
}

func (r *Repository) ListAppIDPage(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	// Probe one row beyond the effective limit: only a real extra record
	// produces a next cursor, so an exactly-full final page yields none.
	appIDs, err := r.queries.ListAppIDPage(ctx, appdb.ListAppIDPageParams{
		OwnerUserID: ownerUserID, Cursor: cursor, RowLimit: int32(limit + 1),
	})
	if err != nil {
		return nil, "", appVersionError("list app ids", err)
	}
	if len(appIDs) <= limit {
		return appIDs, "", nil
	}
	page := appIDs[:limit]
	return page, page[len(page)-1], nil
}

// VisitVersionSummaries streams summaries grouped and ordered by app ID so
// the caller can fold current versions with a fixed-size accumulator. Rows
// are visited as the driver yields them instead of being materialized.
func (r *Repository) VisitVersionSummaries(ctx context.Context, ownerUserID string, appIDs []string, visit func(domain.AppVersionSummary) error) error {
	if len(appIDs) == 0 {
		return nil
	}
	rows, err := r.pool.Query(ctx, summarySelect+`
WHERE owner_user_id = $1 AND app_id = ANY($2::text[])
ORDER BY app_id`, ownerUserID, appIDs)
	if err != nil {
		return appVersionError("list app version summaries", err)
	}
	defer rows.Close()
	for rows.Next() {
		summary, err := scanSummary(rows)
		if err != nil {
			return err
		}
		if err := visit(summary); err != nil {
			return err
		}
	}
	return appVersionError("iterate app version summaries", rows.Err())
}

func scanSummary(rows pgx.Rows) (domain.AppVersionSummary, error) {
	var (
		ownerUserID string
		scope       string
		summary     domain.AppVersionSummary
	)
	if err := rows.Scan(
		&ownerUserID, &summary.AppID, &summary.Version, &scope,
		&summary.Name, &summary.Permissions, &summary.ManifestDigest,
	); err != nil {
		return domain.AppVersionSummary{}, appVersionError("scan app version summary", err)
	}
	summary.Scope = domain.Scope(scope)
	return summary, nil
}

// summaryFromDB strips the manifest from a full-row read: only Register's
// replay and classification paths load full rows, and even they return the
// summary projection.
func summaryFromDB(value appdb.WorkosCoreAppVersion) domain.AppVersionSummary {
	return domain.AppVersionSummary{
		AppID: value.AppID, Version: value.Version, Scope: domain.Scope(value.Scope),
		Name: value.Name, Permissions: value.Permissions, ManifestDigest: value.ManifestDigest,
	}
}

func appVersionError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
