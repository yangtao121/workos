package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/postgres/workspacedb"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
)

func (j *Journal) BeginDelegation(ctx context.Context, d domain.Delegation) (bool, error) {
	n, err := j.queries.BeginDelegatedWorktree(ctx, workspacedb.BeginDelegatedWorktreeParams{DelegationID: d.ID, ParentTaskID: d.TaskID, OwnerUserID: d.OwnerUserID, ProjectID: d.ProjectID, BindingID: d.BindingID, BindingRevision: d.Revision, SourceID: d.SourceID})
	return n == 1, err
}
func (j *Journal) GetDelegation(ctx context.Context, id string) (domain.Delegation, error) {
	d, err := j.queries.GetDelegatedWorktree(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Delegation{}, domain.ErrDenied
	}
	if err != nil {
		return domain.Delegation{}, err
	}
	return domain.Delegation{ID: d.DelegationID, TaskID: d.ParentTaskID, OwnerUserID: d.OwnerUserID, ProjectID: d.ProjectID, BindingID: d.BindingID, Revision: d.BindingRevision, SourceID: d.SourceID, State: d.State, BaseCommit: d.BaseCommit}, nil
}
func (j *Journal) CompleteDelegation(ctx context.Context, id, commit string) error {
	n, err := j.queries.CompleteDelegatedWorktree(ctx, workspacedb.CompleteDelegatedWorktreeParams{DelegationID: id, BaseCommit: commit})
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrUnknownOutcome
	}
	return nil
}
func (j *Journal) ReviewDelegation(ctx context.Context, id string) error {
	return j.queries.ReviewDelegatedWorktree(ctx, id)
}
