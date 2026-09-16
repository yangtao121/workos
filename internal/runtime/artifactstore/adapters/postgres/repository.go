// Package postgres persists artifact provenance (ADR-0033). Each build task
// or owner-scoped import key has its own row; equal bytes may share storage.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/postgres/artifactdb"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/ports"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *artifactdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: artifactdb.New(pool)}
}

var _ ports.MetadataStore = (*Repository)(nil)

func transient(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return domain.ErrStoreUnavailable
}

func (r *Repository) InsertReady(ctx context.Context, artifact domain.Artifact) (domain.Artifact, bool, error) {
	_, err := r.queries.InsertArtifactReady(ctx, artifactdb.InsertArtifactReadyParams{
		ID: artifact.ID, OwnerUserID: artifact.OwnerUserID, Digest: artifact.Digest,
		Format: artifact.Format, SizeBytes: artifact.SizeBytes, FileCount: artifact.FileCount,
		State: artifact.State, ReadyAt: artifact.ReadyAt,
		Origin: artifact.Origin, IdempotencyKey: artifact.IdempotencyKey,
		AppID: nullText(artifact.AppID), TaskID: nullUUID(artifact.TaskID), JobID: nullUUID(artifact.JobID),
		IncidentID: nullUUID(artifact.IncidentID), ProjectID: nullUUID(artifact.ProjectID),
		InstallationID: nullUUID(artifact.InstallationID), SourceBundleID: nullUUID(artifact.SourceBundleID),
		SourceDigest: nullText(artifact.SourceDigest), ManifestDigest: nullText(artifact.ManifestDigest),
		BaseImage: nullText(artifact.BaseImage), BuildCommand: artifact.BuildCommand,
		TestCommand: artifact.TestCommand, OutputDirectory: nullText(artifact.OutputDirectory),
		CreatedAt: artifact.CreatedAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING: the producing task or import key
			// already has metadata; the caller verifies the replay identity.
			var existing domain.Artifact
			var fetchErr error
			if artifact.Origin == domain.OriginBuildJob {
				existing, fetchErr = r.GetByTask(ctx, artifact.TaskID)
			} else {
				existing, fetchErr = r.GetByImportKey(ctx, artifact.OwnerUserID, artifact.IdempotencyKey)
			}
			if fetchErr != nil {
				return domain.Artifact{}, false, fetchErr
			}
			return existing, false, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Concurrent insert under the same (owner, origin, key): the
			// winner's row is the replay target. A miss here means the
			// constraint fired for a reason this store cannot resolve.
			if artifact.TaskID != "" {
				if existing, fetchErr := r.GetByTask(ctx, artifact.TaskID); fetchErr == nil {
					return existing, false, nil
				}
			}
			return domain.Artifact{}, false, domain.ErrConflict
		}
		return domain.Artifact{}, false, transient(err)
	}
	return artifact, true, nil
}

func (r *Repository) GetByID(ctx context.Context, id string) (domain.Artifact, error) {
	row, err := r.queries.GetArtifactById(ctx, id)
	if err != nil {
		return domain.Artifact{}, mapMissing(err)
	}
	return fromRow(row), nil
}

func (r *Repository) GetByOwnerDigest(ctx context.Context, owner, digest string) (domain.Artifact, error) {
	row, err := r.queries.GetArtifactByOwnerDigest(ctx, artifactdb.GetArtifactByOwnerDigestParams{
		OwnerUserID: owner, Digest: digest,
	})
	if err != nil {
		return domain.Artifact{}, mapMissing(err)
	}
	return fromRow(row), nil
}

func (r *Repository) GetByTask(ctx context.Context, taskID string) (domain.Artifact, error) {
	row, err := r.queries.GetArtifactByTask(ctx, uuidValue(taskID))
	if err != nil {
		return domain.Artifact{}, mapMissing(err)
	}
	return fromRow(row), nil
}

func (r *Repository) GetByImportKey(ctx context.Context, owner, key string) (domain.Artifact, error) {
	row, err := r.queries.GetArtifactByImportKey(ctx, artifactdb.GetArtifactByImportKeyParams{
		OwnerUserID: owner, IdempotencyKey: key,
	})
	if err != nil {
		return domain.Artifact{}, mapMissing(err)
	}
	return fromRow(row), nil
}

func (r *Repository) MarkState(ctx context.Context, id, state string) error {
	updated, err := r.queries.MarkArtifactState(ctx, artifactdb.MarkArtifactStateParams{
		ID: id, State: state, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return transient(err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) OwnerUsageBytes(ctx context.Context, owner string) (int64, error) {
	usage, err := r.queries.SumOwnerArtifactBytes(ctx, owner)
	if err != nil {
		return 0, transient(err)
	}
	return usage, nil
}

func (r *Repository) ListByState(ctx context.Context, state string) ([]domain.Artifact, error) {
	rows, err := r.queries.ListArtifactsByState(ctx, state)
	if err != nil {
		return nil, transient(err)
	}
	artifacts := make([]domain.Artifact, 0, len(rows))
	for _, row := range rows {
		artifacts = append(artifacts, fromRow(row))
	}
	return artifacts, nil
}

func mapMissing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return transient(err)
}

func fromRow(row artifactdb.WorkosRuntimeArtifact) domain.Artifact {
	return domain.Artifact{
		ID: row.ID, OwnerUserID: row.OwnerUserID, Digest: row.Digest,
		Format: row.Format, SizeBytes: row.SizeBytes, FileCount: row.FileCount,
		State: row.State, Origin: row.Origin, IdempotencyKey: row.IdempotencyKey,
		AppID: row.AppID.String, TaskID: uuidString(row.TaskID), JobID: uuidString(row.JobID),
		IncidentID: uuidString(row.IncidentID), ProjectID: uuidString(row.ProjectID),
		InstallationID: uuidString(row.InstallationID), SourceBundleID: uuidString(row.SourceBundleID),
		SourceDigest: row.SourceDigest.String, ManifestDigest: row.ManifestDigest.String,
		BaseImage: row.BaseImage.String, BuildCommand: row.BuildCommand,
		TestCommand: row.TestCommand, OutputDirectory: row.OutputDirectory.String,
		CreatedAt: row.CreatedAt, ReadyAt: row.ReadyAt, UpdatedAt: row.UpdatedAt,
	}
}

func nullText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	parsed := uuid.UUID(value.Bytes)
	return parsed.String()
}

func nullUUID(value string) pgtype.UUID {
	if value == "" {
		return pgtype.UUID{}
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func uuidValue(value string) pgtype.UUID { return nullUUID(value) }
