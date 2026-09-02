// Index-feed reconciliation page adapter (ADR-0013): a stable ordered walk
// over this module's immutable review artifacts, identity facts only. The
// generated row/param shapes come from the module's sqlc package
// (uuid -> string override); the zero cursor opens the first page.
package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/artifact/adapters/postgres/artifactdb"
	"github.com/yangtao121/workos/internal/core/artifact/domain"
)

// ReconcileReviewSourcesPage pages every review artifact in stable
// (created_at, id) order with a one-row probe for the next cursor. An empty
// cursor opens the first page; a malformed cursor is an invalid-input
// failure, never a silent restart.
func (r *Repository) ReconcileReviewSourcesPage(ctx context.Context, cursor string, limit int) ([]domain.ReconcileSource, string, error) {
	if limit <= 0 || limit > 200 {
		return nil, "", domain.ErrReconcileCursor
	}
	cursorAt, cursorID, err := domain.DecodeReconcileCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := r.queries.ReconcileReviewArtifactSources(ctx, artifactdb.ReconcileReviewArtifactSourcesParams{
		CursorCreatedAt: pgtype.Timestamptz{Time: cursorAt, Valid: true},
		CursorID:        cursorUUID(cursorID),
		PageLimit:       int32(limit),
	})
	if err != nil {
		return nil, "", artifactError("reconcile review artifact sources", err)
	}
	if len(rows) <= limit {
		out := make([]domain.ReconcileSource, 0, len(rows))
		for _, row := range rows {
			out = append(out, domain.ReconcileSource{
				ArtifactID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
				ArtifactType: row.Type, Digest: row.Digest, CreatedAt: row.CreatedAt.Time.UTC(),
			})
		}
		return out, "", nil
	}
	page := rows[:limit]
	out := make([]domain.ReconcileSource, 0, len(page))
	for _, row := range page {
		out = append(out, domain.ReconcileSource{
			ArtifactID: row.ID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
			ArtifactType: row.Type, Digest: row.Digest, CreatedAt: row.CreatedAt.Time.UTC(),
		})
	}
	last := page[len(page)-1]
	return out, domain.EncodeReconcileCursor(last.CreatedAt.Time.UTC(), last.ID), nil
}

// cursorUUID renders the decoded cursor id as a uuid text input; the nil
// uuid opens the first page together with the zero timestamp.
func cursorUUID(value string) string {
	if value == "" {
		return uuid.Nil.String()
	}
	return value
}
