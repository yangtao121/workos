package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

func (e *Execution) WithDelegations(store ports.DelegationStore, trees ports.Worktrees) *Execution {
	e.delegations, e.worktrees = store, trees
	return e
}

func validDelegationScope(op domain.Operation) bool {
	for _, id := range []string{op.DelegationID, op.ParentTaskID, op.BindingID} {
		value, err := uuid.Parse(id)
		if err != nil || value.Version() != 7 || value.String() != id {
			return false
		}
	}
	return op.Revision > 0
}

func (e *Execution) delegation(ctx context.Context, root string, readOnly bool, op domain.Operation) (ports.Worktree, domain.Result, error) {
	if !validDelegationScope(op) {
		return ports.Worktree{}, nil, domain.ErrInvalid
	}
	if e.delegations == nil || e.worktrees == nil {
		return ports.Worktree{}, nil, domain.ErrUnavailable
	}
	if readOnly {
		return ports.Worktree{}, nil, domain.ErrDenied
	}
	if op.Name == "delegation.create" {
		fresh, err := e.delegations.BeginDelegation(ctx, domain.Delegation{ID: op.DelegationID, TaskID: op.ParentTaskID, OwnerUserID: op.OwnerUserID, ProjectID: op.ProjectID, BindingID: op.BindingID, SourceID: op.SourceID, Revision: op.Revision})
		if err != nil {
			return ports.Worktree{}, nil, err
		}
		if fresh {
			tree, err := e.worktrees.Prepare(ctx, root, op)
			if err != nil {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = e.delegations.ReviewDelegation(cleanup, op.DelegationID)
				return ports.Worktree{}, nil, err
			}
			if err := e.delegations.CompleteDelegation(ctx, op.DelegationID, tree.BaseCommit); err != nil {
				return ports.Worktree{}, nil, err
			}
		}
	}
	saved, err := e.delegations.GetDelegation(ctx, op.DelegationID)
	if err != nil {
		return ports.Worktree{}, nil, err
	}
	if !saved.Matches(op) {
		return ports.Worktree{}, nil, domain.ErrDenied
	}
	if op.Name == "delegation.inspect" {
		return ports.Worktree{}, domain.Result{"worktreeId": saved.ID, "state": saved.State, "baseCommit": saved.BaseCommit}, nil
	}
	if saved.State != "ready" {
		return ports.Worktree{}, nil, domain.ErrUnknownOutcome
	}
	tree, err := e.worktrees.Open(ctx, saved)
	if err != nil {
		return ports.Worktree{}, nil, err
	}
	if op.Name == "delegation.create" {
		return tree, domain.Result{"worktreeId": saved.ID, "state": saved.State, "baseCommit": saved.BaseCommit}, nil
	}
	if op.Name == "delegation.diff" {
		result, err := e.worktrees.Diff(ctx, tree, op)
		return tree, result, err
	}
	return tree, nil, nil
}
