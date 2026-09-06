package postgres

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/core/artifact/adapters/postgres/artifactdb"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// The app lock bounds quota and serializes key adjudication across Core instances.
func (r *Repository) FindAppOutput(ctx context.Context, tx dbtx.Tx, appID, key string) (ports.AppOutputRecord, bool, error) {
	q := r.queries.WithTx(tx)
	if err := q.LockAppArtifactWrites(ctx, appID); err != nil {
		return ports.AppOutputRecord{}, false, artifactError("lock app outputs", err)
	}
	row, err := q.GetAppArtifactRequest(ctx, artifactdb.GetAppArtifactRequestParams{AppInstanceID: appID, OutputKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.AppOutputRecord{}, false, nil
	}
	if err != nil {
		return ports.AppOutputRecord{}, false, artifactError("read app output", err)
	}
	if !domain.ValidArtifactUUID(row.ArtifactID) || !domain.ValidArtifactUUID(row.OwnerUserID) || !domain.ValidArtifactUUID(row.ProjectID) || !domain.ValidArtifactDigest(row.RequestDigest) {
		return ports.AppOutputRecord{}, false, domain.ErrCorrupt
	}
	return ports.AppOutputRecord{ArtifactID: row.ArtifactID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID, RequestDigest: row.RequestDigest}, true, nil
}

func (r *Repository) InsertAppOutput(ctx context.Context, tx dbtx.Tx, command ports.AppOutputCommand) error {
	a := command.Artifact
	if !domain.ValidStoredReviewFact(a) || a.SourceTask != "" || !domain.ValidArtifactDigest(command.RequestDigest) {
		return domain.ErrCorrupt
	}
	normalized, err := domain.NormalizeReviewContent(a.Type, command.Content)
	if err != nil || !bytes.Equal(normalized.Content, command.Content) || normalized.Digest != a.Digest || normalized.ByteCount != a.ByteCount || normalized.LineCount != a.LineCount || command.RequestDigest != domain.ReviewOutputRequestDigest(a.ProjectID, a.SourceAppInstanceID, a.OutputKey, a.Title, a.Digest) {
		return domain.ErrCorrupt
	}
	q := r.queries.WithTx(tx)
	count, err := q.CountAppArtifacts(ctx, a.SourceAppInstanceID)
	if err != nil {
		return artifactError("count app outputs", err)
	}
	if count >= 100 {
		return domain.ErrQuota
	}
	if err := q.InsertAppArtifactRequest(ctx, artifactdb.InsertAppArtifactRequestParams{
		AppInstanceID: a.SourceAppInstanceID, OutputKey: a.OutputKey, OwnerUserID: a.OwnerUserID,
		ProjectID: a.ProjectID, ArtifactID: a.ID, RequestDigest: command.RequestDigest,
	}); err != nil {
		return artifactError("insert app output key", err)
	}
	return artifactError("insert app review", q.InsertAppReviewArtifact(ctx, artifactdb.InsertAppReviewArtifactParams{
		ID: a.ID, OwnerUserID: a.OwnerUserID, ProjectID: a.ProjectID, SourceAppInstanceID: nullableUUID(a.SourceAppInstanceID),
		Type: a.Type, Title: a.Title, MediaType: a.MediaType, Digest: a.Digest, OutputKey: a.OutputKey,
		ByteCount: int32(a.ByteCount), LineCount: int32(a.LineCount), Content: command.Content, CreatedAt: timestamp(a.CreatedAt),
	}))
}
