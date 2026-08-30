// Review artifact storage: typed owner-scoped content reads, project-scoped
// pages, and the transaction-scoped adjudication the neutral materialization
// coordinator drives. This adapter writes only Artifact-owned tables inside
// the shared transaction.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/artifact/adapters/postgres/artifactdb"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// GetReviewContent returns one owner-scoped review artifact's metadata and
// exact canonical content bytes from the same row snapshot. The stored row is
// revalidated on every read and the content digest is recomputed from the
// stored bytes: immutable rows cannot drift, so any drift is ErrCorrupt.
func (r *Repository) GetReviewContent(ctx context.Context, ownerUserID, artifactID string) (domain.ReviewArtifact, domain.NormalizedReviewContent, error) {
	stored, err := r.queries.GetReviewArtifactContent(ctx, artifactdb.GetReviewArtifactContentParams{
		OwnerUserID: ownerUserID, ArtifactID: artifactID,
	})
	if err != nil {
		return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, artifactError("query review artifact content", err)
	}
	artifact := reviewFactFromModel(stored)
	if !domain.ValidStoredReviewFact(artifact) {
		return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrCorrupt
	}
	normalized, err := domain.NormalizeReviewContent(artifact.Type, stored.Content)
	if err != nil || normalized.Digest != artifact.Digest ||
		normalized.ByteCount != artifact.ByteCount || normalized.LineCount != artifact.LineCount {
		return domain.ReviewArtifact{}, domain.NormalizedReviewContent{}, domain.ErrCorrupt
	}
	return artifact, normalized, nil
}

func (r *Repository) ListProjectReviewIDsPage(ctx context.Context, ownerUserID, projectID, cursor string, limit int) ([]string, string, error) {
	ids, err := r.queries.ListProjectReviewArtifactIDPage(ctx, artifactdb.ListProjectReviewArtifactIDPageParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, Cursor: cursor, RowLimit: int32(limit + 1),
	})
	if err != nil {
		return nil, "", artifactError("list project review artifact ids", err)
	}
	if len(ids) <= limit {
		return ids, "", nil
	}
	page := ids[:limit]
	return page, page[len(page)-1], nil
}

// FindTaskOutput reads one adjudication mapping inside the coordinator's
// transaction.
func (r *Repository) FindTaskOutput(ctx context.Context, tx dbtx.Tx, taskID, outputKey string) (ports.TaskOutputRecord, bool, error) {
	queries := r.queries.WithTx(tx)
	row, err := queries.GetReviewArtifactOutput(ctx, artifactdb.GetReviewArtifactOutputParams{
		TaskID: taskID, OutputKey: outputKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.TaskOutputRecord{}, false, nil
	}
	if err != nil {
		return ports.TaskOutputRecord{}, false, storeError("query review artifact output", err)
	}
	return ports.TaskOutputRecord{
		RequestDigest: row.RequestDigest,
		ArtifactID:    row.ArtifactID,
		ArtifactType:  row.ArtifactType,
		Publication: domain.PublicationRecord{
			EventID: row.EventID, EventSeq: row.EventSequence, OccurredAt: row.EventOccurredAt.Time,
		},
	}, true, nil
}

// InsertTaskOutput persists the immutable artifact row and the adjudication
// mapping inside the coordinator's transaction. The ON CONFLICT DO NOTHING
// insert is the physical arbiter for both the (task, output key) identity and
// the (task, type) slot; zero rows tells the coordinator to re-classify.
func (r *Repository) InsertTaskOutput(ctx context.Context, tx dbtx.Tx, command ports.ReviewOutputCommand) (int64, error) {
	queries := r.queries.WithTx(tx)
	if err := queries.InsertReviewArtifact(ctx, artifactdb.InsertReviewArtifactParams{
		ID: command.Artifact.ID, OwnerUserID: command.Artifact.OwnerUserID,
		Type: command.Artifact.Type, Title: command.Artifact.Title,
		MediaType: command.Artifact.MediaType, Digest: command.Artifact.Digest,
		ProjectID: command.Artifact.ProjectID, SourceTaskID: command.Artifact.SourceTask,
		OutputKey: command.Artifact.OutputKey, ByteCount: int32(command.Artifact.ByteCount),
		LineCount: int32(command.Artifact.LineCount), Content: command.Content,
		CreatedAt: timestamp(command.Artifact.CreatedAt),
	}); err != nil {
		return 0, storeError("insert review artifact", err)
	}
	rows, err := queries.InsertReviewArtifactOutput(ctx, artifactdb.InsertReviewArtifactOutputParams{
		TaskID: command.Artifact.SourceTask, OutputKey: command.Artifact.OutputKey,
		ArtifactType: command.Artifact.Type, RequestDigest: command.RequestDigest,
		OwnerUserID: command.Artifact.OwnerUserID, ProjectID: command.Artifact.ProjectID,
		ArtifactID: command.Artifact.ID, EventID: command.Publication.EventID,
		EventSequence:   command.Publication.EventSeq,
		EventOccurredAt: timestamp(command.Publication.OccurredAt),
		CreatedAt:       timestamp(command.Artifact.CreatedAt),
	})
	if err != nil {
		return 0, storeError("insert review artifact output", err)
	}
	return rows, nil
}

// ReviewArtifactByID reads one stored review artifact row inside the
// coordinator's transaction for replay verification. The caller validates the
// lease-derived owner/project/task binding; any grammar drift is ErrCorrupt.
func (r *Repository) ReviewArtifactByID(ctx context.Context, tx dbtx.Tx, artifactID string) (domain.ReviewArtifact, error) {
	queries := r.queries.WithTx(tx)
	stored, err := queries.GetReviewFact(ctx, artifactID)
	if err != nil {
		return domain.ReviewArtifact{}, artifactError("query review fact", err)
	}
	artifact := reviewFactFromRow(stored)
	if !domain.ValidStoredReviewFact(artifact) {
		return domain.ReviewArtifact{}, domain.ErrCorrupt
	}
	return artifact, nil
}

func reviewFactFromRow(row artifactdb.GetReviewFactRow) domain.ReviewArtifact {
	return domain.ReviewArtifact{
		ID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		SourceTask: row.SourceTaskID, OutputKey: row.OutputKey, Type: row.Type,
		Title: row.Title, MediaType: row.MediaType, Digest: row.Digest,
		ByteCount: int(row.ByteCount), LineCount: int(row.LineCount),
		CreatedAt: row.CreatedAt.Time,
	}
}

func reviewFactFromModel(row artifactdb.WorkosCoreProjectReviewArtifact) domain.ReviewArtifact {
	return domain.ReviewArtifact{
		ID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		SourceTask: row.SourceTaskID, OutputKey: row.OutputKey, Type: row.Type,
		Title: row.Title, MediaType: row.MediaType, Digest: row.Digest,
		ByteCount: int(row.ByteCount), LineCount: int(row.LineCount),
		CreatedAt: row.CreatedAt.Time,
	}
}

func artifactFromUnion(row artifactdb.GetArtifactMetadataUnionRow) domain.Artifact {
	return unionArtifact(row.ID, row.OwnerUserID, row.Type, row.Title, row.MediaType,
		row.ContentRef, row.Digest, row.Entrypoint, row.FileCount, row.TotalSizeBytes,
		row.CreatedAt, row.ProjectID, row.SourceTaskID)
}

func artifactFromSummariesUnion(row artifactdb.ListArtifactSummariesUnionRow) domain.Artifact {
	return unionArtifact(row.ID, row.OwnerUserID, row.Type, row.Title, row.MediaType,
		row.ContentRef, row.Digest, row.Entrypoint, row.FileCount, row.TotalSizeBytes,
		row.CreatedAt, row.ProjectID, row.SourceTaskID)
}

func unionArtifact(id, ownerUserID, artifactType, title, mediaType, contentRef, digest, entrypoint string,
	fileCount int32, totalSizeBytes int64, createdAt pgtype.Timestamptz, projectID, sourceTaskID pgtype.UUID,
) domain.Artifact {
	return domain.Artifact{
		ID: id, OwnerUserID: ownerUserID, Type: artifactType, Title: title,
		MediaType: mediaType, ContentRef: contentRef, Digest: digest,
		Entrypoint: entrypoint, FileCount: int(fileCount),
		TotalSizeBytes: totalSizeBytes, CreatedAt: createdAt.Time,
		ProjectID:    uuidText(projectID),
		SourceTaskID: uuidText(sourceTaskID),
	}
}

func uuidText(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}
