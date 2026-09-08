package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/appregistry/adapters/postgres/appdb"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

func (r *Repository) FindCandidateVersion(ctx context.Context, tx dbtx.Tx, taskID string) (ports.CandidateVersionMapping, error) {
	row, err := r.queries.WithTx(tx).FindRepairCandidateVersion(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.CandidateVersionMapping{}, domain.ErrNotFound
		}
		return ports.CandidateVersionMapping{}, storeError("read repair candidate version", err)
	}
	return ports.CandidateVersionMapping{
		TaskID: row.TaskID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		InstallationID: row.InstallationID, IncidentID: row.IncidentID, BuildJobID: row.BuildJobID,
		SourceDigest: row.SourceDigest, AppVersionID: row.AppVersionID, PublishedAt: timePtr(row.PublishedAt),
	}, nil
}

func (r *Repository) GetVersionAnyState(ctx context.Context, tx dbtx.Tx, versionID string) (ports.StagedVersion, error) {
	row, err := r.queries.WithTx(tx).GetAppVersionByIDAnyState(ctx, versionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.StagedVersion{}, domain.ErrNotFound
		}
		return ports.StagedVersion{}, storeError("read app version state", err)
	}
	return ports.StagedVersion{
		AppID: row.AppID, Version: row.Version, ManifestDigest: row.ManifestDigest, State: row.State,
	}, nil
}

func (r *Repository) InsertStagedVersion(ctx context.Context, tx dbtx.Tx, version domain.AppVersion, createdAt time.Time) (string, error) {
	inserted, err := r.queries.WithTx(tx).InsertStagedAppVersion(ctx, appdb.InsertStagedAppVersionParams{
		ID: version.ID, OwnerUserID: version.OwnerUserID, AppID: version.AppID,
		Version: version.Version, Scope: string(version.Scope), Name: version.Name,
		Permissions: version.Permissions, ManifestDigest: version.ManifestDigest,
		CanonicalManifest: version.CanonicalManifest, CreatedAt: timestamp(createdAt),
	})
	if err != nil {
		return "", storeError("insert staged app version", err)
	}
	if inserted == 0 {
		// The unique (owner, app, version) identity already exists: either a
		// concurrent registration of this task (fine, caller replays by task)
		// or a genuine label collision (caller surfaces the stable conflict).
		return "", domain.ErrIdempotencyConflict
	}
	return version.ID, nil
}

func (r *Repository) InsertCandidateVersion(ctx context.Context, tx dbtx.Tx, mapping ports.CandidateVersionMapping, createdAt time.Time) error {
	inserted, err := r.queries.WithTx(tx).InsertRepairCandidateVersion(ctx, appdb.InsertRepairCandidateVersionParams{
		TaskID: mapping.TaskID, OwnerUserID: mapping.OwnerUserID, ProjectID: mapping.ProjectID,
		InstallationID: mapping.InstallationID, IncidentID: mapping.IncidentID,
		BuildJobID: mapping.BuildJobID, SourceDigest: mapping.SourceDigest,
		AppVersionID: mapping.AppVersionID, CreatedAt: timestamp(createdAt),
	})
	if err != nil {
		return storeError("insert repair candidate version", err)
	}
	if inserted == 0 {
		return domain.ErrIdempotencyConflict
	}
	return nil
}

func (r *Repository) PublishStagedVersion(ctx context.Context, tx dbtx.Tx, taskID, versionID string, publishedAt time.Time) (bool, error) {
	updated, err := r.queries.WithTx(tx).PublishStagedAppVersion(ctx, versionID)
	if err != nil {
		return false, storeError("publish staged app version", err)
	}
	if updated == 0 {
		// Already published (or gone): a deterministic replay, never an
		// error and never a second publish side effect.
		return false, nil
	}
	if _, err := r.queries.WithTx(tx).MarkCandidateVersionPublished(ctx, appdb.MarkCandidateVersionPublishedParams{TaskID: taskID, PublishedAt: timestamp(publishedAt)}); err != nil {
		return false, storeError("mark candidate version published", err)
	}
	return true, nil
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	stamp := value.Time.UTC()
	return &stamp
}
