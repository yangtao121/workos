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
// is decided by the table's unique constraints, never by process state: the
// loser of a race re-reads by (owner, app, version) and (owner, idempotency
// key) inside the same transaction and returns either the stored immutable
// fact or a deterministic conflict.
type Repository struct {
	pool    *pgxpool.Pool
	queries *appdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: appdb.New(pool)}
}

func (r *Repository) Register(ctx context.Context, record domain.AppVersion) (domain.AppVersion, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.AppVersion{}, fmt.Errorf("begin register app version: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	existing, err := queries.GetAppVersionByIdempotency(ctx, appdb.GetAppVersionByIdempotencyParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
	})
	if err == nil {
		if existing.RequestDigest != record.RequestDigest {
			return domain.AppVersion{}, domain.ErrIdempotencyConflict
		}
		return appVersionFromDB(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AppVersion{}, fmt.Errorf("query app idempotency: %w", err)
	}

	rows, err := queries.InsertAppVersion(ctx, appdb.InsertAppVersionParams{
		ID: record.ID, OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
		RequestDigest: record.RequestDigest, AppID: record.AppID, Version: record.Version,
		Scope: string(record.Scope), Name: record.Name, Permissions: record.Permissions,
		ManifestDigest: record.ManifestDigest, CanonicalManifest: record.CanonicalManifest,
		CreatedAt: timestamp(record.CreatedAt),
	})
	if err != nil {
		return domain.AppVersion{}, fmt.Errorf("insert app version: %w", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.AppVersion{}, fmt.Errorf("commit register app version: %w", err)
		}
		return record, nil
	}

	byVersion, err := queries.GetAppVersion(ctx, appdb.GetAppVersionParams{
		OwnerUserID: record.OwnerUserID, AppID: record.AppID, Version: record.Version,
	})
	if err == nil {
		if byVersion.ManifestDigest == record.ManifestDigest {
			return appVersionFromDB(byVersion), nil
		}
		return domain.AppVersion{}, domain.ErrVersionExists
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AppVersion{}, fmt.Errorf("query conflicting app version: %w", err)
	}

	byKey, err := queries.GetAppVersionByIdempotency(ctx, appdb.GetAppVersionByIdempotencyParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
	})
	if err == nil {
		if byKey.RequestDigest == record.RequestDigest {
			return appVersionFromDB(byKey), nil
		}
		return domain.AppVersion{}, domain.ErrIdempotencyConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.AppVersion{}, fmt.Errorf("query conflicting app idempotency: %w", err)
	}
	return domain.AppVersion{}, fmt.Errorf("register app version: insert conflict could not be classified")
}

func (r *Repository) GetVersion(ctx context.Context, ownerUserID, appID, version string) (domain.AppVersion, error) {
	value, err := r.queries.GetAppVersion(ctx, appdb.GetAppVersionParams{
		OwnerUserID: ownerUserID, AppID: appID, Version: version,
	})
	return appVersionFromDB(value), appVersionError("query app version", err)
}

func (r *Repository) GetAppVersions(ctx context.Context, ownerUserID, appID string) ([]domain.AppVersion, error) {
	values, err := r.queries.GetAppVersions(ctx, appdb.GetAppVersionsParams{
		OwnerUserID: ownerUserID, AppID: appID,
	})
	if err != nil {
		return nil, appVersionError("list app versions", err)
	}
	result := make([]domain.AppVersion, 0, len(values))
	for _, value := range values {
		result = append(result, appVersionFromDB(value))
	}
	return result, nil
}

func (r *Repository) ListAppIDs(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, error) {
	return r.queries.ListAppIDs(ctx, appdb.ListAppIDsParams{
		OwnerUserID: ownerUserID, Cursor: cursor, RowLimit: int32(limit),
	})
}

func (r *Repository) GetVersionsForApps(ctx context.Context, ownerUserID string, appIDs []string) ([]domain.AppVersion, error) {
	if len(appIDs) == 0 {
		return nil, nil
	}
	values, err := r.queries.ListAppVersionsForApps(ctx, appdb.ListAppVersionsForAppsParams{
		OwnerUserID: ownerUserID, AppIds: appIDs,
	})
	if err != nil {
		return nil, appVersionError("list app versions for apps", err)
	}
	result := make([]domain.AppVersion, 0, len(values))
	for _, value := range values {
		result = append(result, appVersionFromDB(value))
	}
	return result, nil
}

func appVersionFromDB(value appdb.WorkosCoreAppVersion) domain.AppVersion {
	return domain.AppVersion{
		ID: value.ID, OwnerUserID: value.OwnerUserID, IdempotencyKey: value.IdempotencyKey,
		RequestDigest: value.RequestDigest, AppID: value.AppID, Version: value.Version,
		Scope: domain.Scope(value.Scope), Name: value.Name, Permissions: value.Permissions,
		ManifestDigest: value.ManifestDigest, CanonicalManifest: value.CanonicalManifest,
		CreatedAt: value.CreatedAt.Time,
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
