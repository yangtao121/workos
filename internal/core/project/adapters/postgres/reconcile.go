// Index-feed reconciliation reads over the Project module (ADR-0013): the
// archived-project page for tombstone convergence and the transaction-scoped
// liveness check the publication resolve path uses. Only this module's own
// table is read; no other schema is touched.
package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// projectCursorVersion mirrors the artifact reconcile cursor: versioned,
// (time, id) ordered, fail-closed decode.
const projectCursorVersion = "v1"

func encodeProjectCursor(at time.Time, id string) string {
	return projectCursorVersion + ":" + strconvFormatInt(at.UnixMicro()) + ":" + id
}

func decodeProjectCursor(value string) (time.Time, string, error) {
	parts := splitCursor(value)
	if len(parts) != 3 || parts[0] != projectCursorVersion {
		return time.Time{}, "", domain.ErrInvalid
	}
	at, err := parseMicros(parts[1])
	if err != nil {
		return time.Time{}, "", domain.ErrInvalid
	}
	id := parts[2]
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return time.Time{}, "", domain.ErrInvalid
		}
	}
	return at, id, nil
}

// ReconcileArchivedProjectsPage pages archived projects in stable
// (archived_at, id) order with a one-row probe for the next cursor.
func (r *Repository) ReconcileArchivedProjectsPage(ctx context.Context, cursor string, limit int) ([]projectports.ArchivedProjectRef, string, error) {
	if limit <= 0 || limit > 200 {
		return nil, "", domain.ErrInvalid
	}
	cursorAt, cursorID, err := decodeProjectCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := r.queries.ReconcileArchivedProjects(ctx, projectdb.ReconcileArchivedProjectsParams{
		CursorArchivedAt: pgtype.Timestamptz{Time: cursorAt, Valid: true},
		CursorID:         cursorUUIDText(cursorID),
		PageLimit:        int32(limit),
	})
	if err != nil {
		return nil, "", storeError("reconcile archived projects", err)
	}
	makeRef := func(row projectdb.ReconcileArchivedProjectsRow) projectports.ArchivedProjectRef {
		return projectports.ArchivedProjectRef{
			OwnerUserID: row.OwnerUserID, ProjectID: row.ID, ArchivedAt: row.ArchivedAt.Time.UTC(),
		}
	}
	if len(rows) <= limit {
		out := make([]projectports.ArchivedProjectRef, 0, len(rows))
		for _, row := range rows {
			out = append(out, makeRef(row))
		}
		return out, "", nil
	}
	page := rows[:limit]
	out := make([]projectports.ArchivedProjectRef, 0, len(page))
	for _, row := range page {
		out = append(out, makeRef(row))
	}
	last := page[len(page)-1]
	return out, encodeProjectCursor(last.ArchivedAt.Time.UTC(), last.ID), nil
}

// ReviewProjectActiveTx reports whether the project exists, is owned by the
// caller, and is not archived, inside the caller's transaction. It is the
// publication resolve path's authoritative liveness check: an archive
// committing in the same database serializes against this read.
func (r *Repository) ReviewProjectActiveTx(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID string) (bool, error) {
	row, err := r.queries.WithTx(tx).GetProject(ctx, projectdb.GetProjectParams{
		OwnerUserID: ownerUserID, ID: projectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storeError("review project active", err)
	}
	return !row.ArchivedAt.Valid, nil
}

func cursorUUIDText(value string) string {
	if value == "" {
		return uuid.Nil.String()
	}
	return value
}

func strconvFormatInt(value int64) string { return strconv.FormatInt(value, 10) }

func splitCursor(value string) []string { return strings.Split(value, ":") }

func parseMicros(raw string) (time.Time, error) {
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || micros < 0 {
		return time.Time{}, domain.ErrInvalid
	}
	return time.UnixMicro(micros).UTC(), nil
}
