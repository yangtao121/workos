package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/runtime/previewhost/adapters/postgres/previewdb"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"time"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *previewdb.Queries
}

func New(pool *pgxpool.Pool) *Repository       { return &Repository{pool, previewdb.New(pool)} }
func timestamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func record(r previewdb.WorkosRuntimeWorkspacePreview) ports.PreviewRecord {
	return ports.PreviewRecord{PreviewID: r.PreviewID, OwnerUserID: r.OwnerUserID, ProjectID: r.ProjectID, IdempotencyKey: r.IdempotencyKey, WorkspaceSourceID: r.WorkspaceSourceID, ReadOnly: r.ReadOnly, State: r.State, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time, ExpiresAt: r.ExpiresAt.Time, Command: r.Command, Port: r.Port, Generation: r.Generation, AccessToken: r.AccessToken, RequestDigest: r.RequestDigest, BindingID: r.BindingID, BindingRevision: r.BindingRevision}
}
func (r *Repository) FindByOwnerKey(ctx context.Context, owner, key string) (ports.PreviewRecord, bool, error) {
	row, err := r.queries.GetPreviewByOwnerKey(ctx, previewdb.GetPreviewByOwnerKeyParams{OwnerUserID: owner, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.PreviewRecord{}, false, nil
	}
	return record(row), err == nil, err
}
func (r *Repository) Insert(ctx context.Context, p ports.PreviewRecord) (bool, error) {
	count, err := r.queries.InsertWorkspacePreview(ctx, previewdb.InsertWorkspacePreviewParams{PreviewID: p.PreviewID, OwnerUserID: p.OwnerUserID, ProjectID: p.ProjectID, IdempotencyKey: p.IdempotencyKey, WorkspaceSourceID: p.WorkspaceSourceID, ReadOnly: p.ReadOnly, State: p.State, CreatedAt: timestamp(p.CreatedAt), UpdatedAt: timestamp(p.UpdatedAt), ExpiresAt: timestamp(p.ExpiresAt), Command: p.Command, Port: p.Port, Generation: p.Generation, AccessToken: p.AccessToken, RequestDigest: p.RequestDigest, BindingID: p.BindingID, BindingRevision: p.BindingRevision})
	return count == 1, err
}
func (r *Repository) Get(ctx context.Context, id string) (ports.PreviewRecord, error) {
	row, err := r.queries.GetWorkspacePreview(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return record(row), err
}
func (r *Repository) ListByProject(ctx context.Context, owner, project string, limit int) ([]ports.PreviewRecord, error) {
	rows, err := r.queries.ListProjectWorkspacePreviews(ctx, previewdb.ListProjectWorkspacePreviewsParams{OwnerUserID: owner, ProjectID: project, Limit: int32(limit)})
	out := make([]ports.PreviewRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, record(row))
	}
	return out, err
}
func (r *Repository) ListActive(ctx context.Context) ([]ports.PreviewRecord, error) {
	rows, err := r.queries.ListActiveWorkspacePreviews(ctx)
	out := make([]ports.PreviewRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, record(row))
	}
	return out, err
}
func (r *Repository) UpdateState(ctx context.Context, owner, id, state string, now time.Time) error {
	n, err := r.queries.UpdateWorkspacePreviewState(ctx, previewdb.UpdateWorkspacePreviewStateParams{OwnerUserID: owner, PreviewID: id, State: state, UpdatedAt: timestamp(now)})
	if err == nil && n != 1 {
		return domain.ErrNotFound
	}
	return err
}
func (r *Repository) Activate(ctx context.Context, p ports.PreviewRecord) error {
	n, err := r.queries.ActivateWorkspacePreview(ctx, previewdb.ActivateWorkspacePreviewParams{OwnerUserID: p.OwnerUserID, PreviewID: p.PreviewID, WorkspaceSourceID: p.WorkspaceSourceID, ReadOnly: p.ReadOnly, BindingID: p.BindingID, BindingRevision: p.BindingRevision, UpdatedAt: timestamp(p.UpdatedAt)})
	if err == nil && n != 1 {
		return domain.ErrUnavailable
	}
	return err
}
func (r *Repository) Action(ctx context.Context, owner, id, key, action string, now time.Time) (ports.PreviewRecord, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.PreviewRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	row, err := q.LockWorkspacePreview(ctx, previewdb.LockWorkspacePreviewParams{OwnerUserID: owner, PreviewID: id})
	if err != nil {
		return ports.PreviewRecord{}, false, domain.ErrNotFound
	}
	prior, err := q.GetWorkspacePreviewAction(ctx, previewdb.GetWorkspacePreviewActionParams{PreviewID: id, ActionKey: key})
	if err == nil {
		if prior != action {
			return ports.PreviewRecord{}, false, domain.ErrConflict
		}
		return record(row), false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ports.PreviewRecord{}, false, err
	}
	switch action {
	case "restart":
		row, err = q.RestartWorkspacePreview(ctx, previewdb.RestartWorkspacePreviewParams{OwnerUserID: owner, PreviewID: id, UpdatedAt: timestamp(now), ExpiresAt: timestamp(now.Add(domain.PreviewTTL))})
	case "stop":
		_, err = q.UpdateWorkspacePreviewState(ctx, previewdb.UpdateWorkspacePreviewStateParams{OwnerUserID: owner, PreviewID: id, State: domain.StateStopped, UpdatedAt: timestamp(now)})
		row.State = domain.StateStopped
	default:
		return ports.PreviewRecord{}, false, domain.ErrInvalid
	}
	if err != nil {
		return ports.PreviewRecord{}, false, err
	}
	if err = q.RecordWorkspacePreviewAction(ctx, previewdb.RecordWorkspacePreviewActionParams{PreviewID: id, ActionKey: key, Action: action, Generation: row.Generation}); err != nil {
		return ports.PreviewRecord{}, false, err
	}
	return record(row), true, tx.Commit(ctx)
}
