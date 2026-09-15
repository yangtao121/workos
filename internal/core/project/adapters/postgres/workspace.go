package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

// WorkspaceRepository persists Core-owned workspace binding facts
// (ADR-0030) with the same transient classifier as the project store.
type WorkspaceRepository struct {
	queries *projectdb.Queries
}

func NewWorkspaceRepository(queries *projectdb.Queries) *WorkspaceRepository {
	return &WorkspaceRepository{queries: queries}
}

func workspaceBindingError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w: %w", operation, domain.ErrNotFound, err)
	}
	return storeError(operation, err)
}

func bindingFromRow(row projectdb.WorkosCoreProjectWorkspaceBinding) domain.WorkspaceBinding {
	return domain.WorkspaceBinding{
		ID:                row.BindingID,
		OwnerUserID:       row.OwnerUserID,
		ProjectID:         row.ProjectID,
		WorkspaceSourceID: row.WorkspaceSourceID,
		IdempotencyKey:    row.IdempotencyKey,
		DisplayName:       row.DisplayName,
		ReadOnly:          row.ReadOnly,
		State:             domain.WorkspaceBindingState(row.State),
		Revision:          row.Revision,
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
		ArchivedAt:        timePtr(row.ArchivedAt),
	}
}

// workspaceBindingDigest recomputes the request digest of stored facts so
// replay adjudication compares caller facts against the same formula.
func workspaceBindingDigest(sourceID, displayName string, readOnly bool) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("workspace:%s:%s:%t", sourceID, displayName, readOnly)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *WorkspaceRepository) InsertWorkspaceBinding(ctx context.Context, binding domain.WorkspaceBinding, requestDigest string) (string, bool, error) {
	inserted, err := r.queries.InsertWorkspaceBinding(ctx, projectdb.InsertWorkspaceBindingParams{
		BindingID: binding.ID, OwnerUserID: binding.OwnerUserID, ProjectID: binding.ProjectID,
		WorkspaceSourceID: binding.WorkspaceSourceID, IdempotencyKey: binding.IdempotencyKey,
		DisplayName: binding.DisplayName, ReadOnly: binding.ReadOnly, CreatedAt: timestamp(binding.CreatedAt),
	})
	if err != nil {
		return "", false, workspaceBindingError("insert workspace binding", err)
	}
	if inserted == 0 {
		previous, err := r.queries.GetWorkspaceBindingByIdempotency(ctx, projectdb.GetWorkspaceBindingByIdempotencyParams{
			OwnerUserID: binding.OwnerUserID, IdempotencyKey: binding.IdempotencyKey,
		})
		if err != nil {
			return "", false, workspaceBindingError("load consumed workspace key", err)
		}
		return workspaceBindingDigest(previous.WorkspaceSourceID, previous.DisplayName, previous.ReadOnly), false, nil
	}
	return requestDigest, true, nil
}

func (r *WorkspaceRepository) GetWorkspaceBinding(ctx context.Context, ownerUserID, bindingID string) (domain.WorkspaceBinding, error) {
	row, err := r.queries.GetWorkspaceBinding(ctx, projectdb.GetWorkspaceBindingParams{OwnerUserID: ownerUserID, BindingID: bindingID})
	if err != nil {
		return domain.WorkspaceBinding{}, workspaceBindingError("get workspace binding", err)
	}
	return bindingFromRow(row), nil
}

func (r *WorkspaceRepository) GetWorkspaceBindingByIdempotency(ctx context.Context, ownerUserID, idempotencyKey string) (domain.WorkspaceBinding, error) {
	row, err := r.queries.GetWorkspaceBindingByIdempotency(ctx, projectdb.GetWorkspaceBindingByIdempotencyParams{OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey})
	if err != nil {
		return domain.WorkspaceBinding{}, workspaceBindingError("get workspace binding by key", err)
	}
	return bindingFromRow(row), nil
}

func (r *WorkspaceRepository) GetActiveWorkspaceBindingForProject(ctx context.Context, ownerUserID, projectID string) (domain.WorkspaceBinding, error) {
	row, err := r.queries.GetActiveWorkspaceBindingForProject(ctx, projectdb.GetActiveWorkspaceBindingForProjectParams{OwnerUserID: ownerUserID, ProjectID: projectID})
	if err != nil {
		return domain.WorkspaceBinding{}, workspaceBindingError("get active workspace binding", err)
	}
	return bindingFromRow(row), nil
}

func (r *WorkspaceRepository) ListWorkspaceBindings(ctx context.Context, ownerUserID, projectID string, includeArchived bool) ([]domain.WorkspaceBinding, error) {
	rows, err := r.queries.ListWorkspaceBindings(ctx, projectdb.ListWorkspaceBindingsParams{OwnerUserID: ownerUserID, ProjectID: projectID})
	if err != nil {
		return nil, workspaceBindingError("list workspace bindings", err)
	}
	out := make([]domain.WorkspaceBinding, 0, len(rows))
	for _, row := range rows {
		if !includeArchived && row.State == string(domain.WorkspaceBindingArchived) {
			continue
		}
		out = append(out, domain.WorkspaceBinding{
			ID: row.BindingID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
			WorkspaceSourceID: row.WorkspaceSourceID, IdempotencyKey: row.IdempotencyKey,
			DisplayName: row.DisplayName, ReadOnly: row.ReadOnly,
			State: domain.WorkspaceBindingState(row.State), Revision: row.Revision,
			CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, ArchivedAt: timePtr(row.ArchivedAt),
		})
	}
	return out, nil
}

func (r *WorkspaceRepository) UpdateWorkspaceAccess(ctx context.Context, ownerUserID, bindingID string, readOnly bool, expectedRevision int64, now time.Time) (domain.WorkspaceBinding, error) {
	updated, err := r.queries.UpdateWorkspaceAccess(ctx, projectdb.UpdateWorkspaceAccessParams{
		OwnerUserID: ownerUserID, BindingID: bindingID, ReadOnly: readOnly,
		UpdatedAt: timestamp(now), Revision: expectedRevision,
	})
	if err != nil {
		return domain.WorkspaceBinding{}, workspaceBindingError("update workspace access", err)
	}
	if updated == 0 {
		return domain.WorkspaceBinding{}, fmt.Errorf("update workspace access: %w", domain.ErrNotFound)
	}
	return r.GetWorkspaceBinding(ctx, ownerUserID, bindingID)
}

func (r *WorkspaceRepository) ArchiveWorkspaceBinding(ctx context.Context, ownerUserID, bindingID string, expectedRevision int64, now time.Time) (domain.WorkspaceBinding, error) {
	archived, err := r.queries.ArchiveWorkspaceBinding(ctx, projectdb.ArchiveWorkspaceBindingParams{
		OwnerUserID: ownerUserID, BindingID: bindingID, UpdatedAt: timestamp(now), Revision: expectedRevision,
	})
	if err != nil {
		return domain.WorkspaceBinding{}, workspaceBindingError("archive workspace binding", err)
	}
	if archived == 0 {
		return domain.WorkspaceBinding{}, fmt.Errorf("archive workspace binding: %w", domain.ErrNotFound)
	}
	return r.GetWorkspaceBinding(ctx, ownerUserID, bindingID)
}

var _ ports.WorkspaceRepository = (*WorkspaceRepository)(nil)
