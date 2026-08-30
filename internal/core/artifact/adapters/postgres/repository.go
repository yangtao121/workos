// Package postgres adapts the Artifact module's storage port to Core-owned
// PostgreSQL tables. Metadata, files, and the idempotency mapping commit in
// one transaction; same-key races are arbitrated by the request-mapping
// primary key, never by process state.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/artifact/adapters/postgres/artifactdb"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *artifactdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: artifactdb.New(pool)}
}

// Create persists the immutable artifact, all normalized files, and the
// idempotency mapping in one transaction. An already-consumed key rules
// first: the identical canonical request replays the stored artifact, any
// different request conflicts. A concurrent winner of the same key forces
// the loser to re-read and classify; the rolled-back loser leaves no orphan
// rows.
func (r *Repository) Create(ctx context.Context, command ports.CreateCommand) (domain.Artifact, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Artifact{}, storeError("begin create artifact", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	request, err := queries.GetArtifactRequest(ctx, artifactdb.GetArtifactRequestParams{
		OwnerUserID: command.Artifact.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	if err == nil {
		if request.RequestDigest != command.RequestDigest {
			return domain.Artifact{}, domain.ErrIdempotencyConflict
		}
		stored, err := queries.GetArtifact(ctx, artifactdb.GetArtifactParams{
			OwnerUserID: command.Artifact.OwnerUserID, ID: request.ArtifactID,
		})
		if err != nil {
			return domain.Artifact{}, storeError("query idempotent artifact", err)
		}
		return artifactFromDB(stored), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Artifact{}, storeError("query artifact request", err)
	}

	if err := insertArtifact(ctx, queries, command.Artifact); err != nil {
		return domain.Artifact{}, err
	}
	for _, file := range command.Bundle.Files {
		if err := queries.InsertBundleFile(ctx, artifactdb.InsertBundleFileParams{
			ArtifactID: command.Artifact.ID, Path: file.Path, MediaType: file.MediaType,
			SizeBytes: int32(file.SizeBytes), Digest: file.FileDigest, Content: file.Content,
		}); err != nil {
			return domain.Artifact{}, storeError("insert bundle file", err)
		}
	}
	return r.consumeKey(ctx, tx, queries, command)
}

// consumeKey inserts the request mapping and commits. If another transaction
// consumed the same key first, the loser re-reads the mapping: the identical
// request replays, anything else conflicts.
func (r *Repository) consumeKey(ctx context.Context, tx pgx.Tx, queries *artifactdb.Queries, command ports.CreateCommand) (domain.Artifact, error) {
	rows, err := queries.InsertArtifactRequest(ctx, artifactdb.InsertArtifactRequestParams{
		OwnerUserID: command.Artifact.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		RequestDigest: command.RequestDigest, ArtifactID: command.Artifact.ID,
		CreatedAt: timestamp(command.Artifact.CreatedAt),
	})
	if err != nil {
		return domain.Artifact{}, storeError("insert artifact request", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.Artifact{}, storeError("commit create artifact", err)
		}
		return command.Artifact, nil
	}
	consumed, err := queries.GetArtifactRequest(ctx, artifactdb.GetArtifactRequestParams{
		OwnerUserID: command.Artifact.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	if err != nil {
		return domain.Artifact{}, storeError("classify consumed artifact request", err)
	}
	if consumed.RequestDigest != command.RequestDigest {
		return domain.Artifact{}, domain.ErrIdempotencyConflict
	}
	stored, err := queries.GetArtifact(ctx, artifactdb.GetArtifactParams{
		OwnerUserID: command.Artifact.OwnerUserID, ID: consumed.ArtifactID,
	})
	if err != nil {
		return domain.Artifact{}, storeError("query consumed artifact", err)
	}
	return artifactFromDB(stored), nil
}

func insertArtifact(ctx context.Context, queries *artifactdb.Queries, artifact domain.Artifact) error {
	err := queries.InsertArtifact(ctx, artifactdb.InsertArtifactParams{
		ID: artifact.ID, OwnerUserID: artifact.OwnerUserID, Type: artifact.Type,
		Title: artifact.Title, MediaType: artifact.MediaType, ContentRef: artifact.ContentRef,
		Digest: artifact.Digest, Entrypoint: artifact.Entrypoint,
		FileCount: int32(artifact.FileCount), TotalSizeBytes: artifact.TotalSizeBytes,
		CreatedAt: timestamp(artifact.CreatedAt),
	})
	if err != nil {
		return storeError("insert artifact", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, ownerUserID, artifactID string) (domain.Artifact, error) {
	stored, err := r.queries.GetArtifactMetadataUnion(ctx, artifactdb.GetArtifactMetadataUnionParams{
		OwnerUserID: ownerUserID, ArtifactID: artifactID,
	})
	if err != nil {
		return domain.Artifact{}, artifactError("query artifact", err)
	}
	artifact := artifactFromUnion(stored)
	if !domain.ValidStoredArtifact(artifact) {
		return domain.Artifact{}, domain.ErrCorrupt
	}
	return artifact, nil
}

func (r *Repository) ListIDsPage(ctx context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	// Probe one row beyond the effective limit so only a real extra record
	// produces a next cursor. The page spans both implemented subtypes in
	// one ordered union.
	ids, err := r.queries.ListArtifactIDPageUnion(ctx, artifactdb.ListArtifactIDPageUnionParams{
		OwnerUserID: ownerUserID, Cursor: cursor, RowLimit: int32(limit + 1),
	})
	if err != nil {
		return nil, "", artifactError("list artifact ids", err)
	}
	if len(ids) <= limit {
		return ids, "", nil
	}
	page := ids[:limit]
	return page, page[len(page)-1], nil
}

func (r *Repository) VisitSummaries(ctx context.Context, ownerUserID string, ids []string, visit func(domain.Artifact) error) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.queries.ListArtifactSummariesUnion(ctx, artifactdb.ListArtifactSummariesUnionParams{
		OwnerUserID: ownerUserID, Ids: ids,
	})
	if err != nil {
		return artifactError("list artifact summaries", err)
	}
	for _, row := range rows {
		artifact := artifactFromSummariesUnion(row)
		if !domain.ValidStoredArtifact(artifact) {
			return domain.ErrCorrupt
		}
		if err := visit(artifact); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ReadAsset(ctx context.Context, ownerUserID, artifactID, path string) (domain.BundleFile, error) {
	stored, err := r.queries.ReadBundleAsset(ctx, artifactdb.ReadBundleAssetParams{
		OwnerUserID: ownerUserID, ID: artifactID, Path: path,
	})
	if err != nil {
		return domain.BundleFile{}, artifactError("read bundle asset", err)
	}
	return domain.BundleFile{
		Path: stored.Path, MediaType: stored.MediaType, Content: stored.Content,
		SizeBytes: int(stored.SizeBytes), FileDigest: stored.Digest,
	}, nil
}

func artifactFromDB(row artifactdb.WorkosCoreWebBundleArtifact) domain.Artifact {
	return domain.Artifact{
		ID: row.ID, OwnerUserID: row.OwnerUserID, Type: row.Type, Title: row.Title,
		MediaType: row.MediaType, ContentRef: row.ContentRef, Digest: row.Digest,
		Entrypoint: row.Entrypoint, FileCount: int(row.FileCount),
		TotalSizeBytes: row.TotalSizeBytes, CreatedAt: row.CreatedAt.Time,
	}
}

func artifactError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return storeError(operation, err)
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

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
