package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"time"
)

func interaction(row agentdb.WorkosCoreAgentExecutionInteraction) (domain.ExecutionInteraction, error) {
	r := domain.ExecutionInteraction{ID: row.ID, TaskID: row.TaskID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID, LeaseID: row.LeaseID, WorkerID: row.WorkerID, RequestKey: row.RequestKey, State: row.State, DecisionKey: row.DecisionKey, DecisionDigest: row.DecisionDigest, CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time}
	if err := json.Unmarshal(row.Questions, &r.Questions); err != nil {
		return r, err
	}
	if err := json.Unmarshal(row.Answers, &r.Answers); err != nil {
		return r, err
	}
	return r, nil
}
func (r *Repository) CreateInteraction(ctx context.Context, in domain.ExecutionInteraction) (domain.ExecutionInteraction, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return in, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	facts, err := r.LockTaskArtifactStream(ctx, tx, in.LeaseID, in.WorkerID, time.Now().UTC())
	if err != nil {
		return in, err
	}
	if facts.OwnerUserID != in.OwnerUserID || facts.TaskID != in.TaskID || facts.ProjectID != in.ProjectID || facts.CancellationRequested {
		return in, domain.ErrLeaseLost
	}
	data, err := json.Marshal(in.Questions)
	if err != nil {
		return in, err
	}
	_, err = q.InsertExecutionInteraction(ctx, agentdb.InsertExecutionInteractionParams{ID: in.ID, TaskID: in.TaskID, OwnerUserID: in.OwnerUserID, ProjectID: in.ProjectID, LeaseID: in.LeaseID, WorkerID: in.WorkerID, RequestKey: in.RequestKey, Questions: data, CreatedAt: timestamp(in.CreatedAt), ExpiresAt: timestamp(in.ExpiresAt)})
	if err != nil {
		return in, err
	}
	row, err := q.GetExecutionInteractionByKey(ctx, agentdb.GetExecutionInteractionByKeyParams{TaskID: in.TaskID, RequestKey: in.RequestKey})
	if err != nil {
		return in, err
	}
	stored, err := interaction(row)
	if err != nil {
		return in, err
	}
	previous, _ := json.Marshal(stored.Questions)
	if !bytes.Equal(data, previous) || stored.LeaseID != in.LeaseID || stored.WorkerID != in.WorkerID {
		return in, domain.ErrInvalid
	}
	return stored, tx.Commit(ctx)
}
func (r *Repository) GetInteraction(ctx context.Context, owner, id string) (domain.ExecutionInteraction, error) {
	row, err := r.queries.GetExecutionInteraction(ctx, agentdb.GetExecutionInteractionParams{OwnerUserID: owner, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	if err != nil {
		return domain.ExecutionInteraction{}, err
	}
	return interaction(row)
}
func (r *Repository) ListInteractions(ctx context.Context, owner, task string) ([]domain.ExecutionInteraction, error) {
	rows, err := r.queries.ListExecutionInteractions(ctx, agentdb.ListExecutionInteractionsParams{OwnerUserID: owner, TaskID: task})
	if err != nil {
		return nil, err
	}
	result := make([]domain.ExecutionInteraction, 0, len(rows))
	for _, row := range rows {
		item, err := interaction(row)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
func (r *Repository) ExpireInteraction(ctx context.Context, owner, id string) error {
	return r.queries.ExpireExecutionInteraction(ctx, agentdb.ExpireExecutionInteractionParams{OwnerUserID: owner, ID: id})
}
func (r *Repository) DecideInteraction(ctx context.Context, in domain.ExecutionInteraction, now time.Time) (domain.ExecutionInteraction, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return in, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	// Use task -> interaction lock order, matching CreateInteraction.
	initial, err := q.GetExecutionInteraction(ctx, agentdb.GetExecutionInteractionParams{OwnerUserID: in.OwnerUserID, ID: in.ID})
	if err != nil {
		return in, domain.ErrNotFound
	}
	if initial.State == "pending" {
		facts, err := r.LockTaskArtifactStream(ctx, tx, initial.LeaseID, initial.WorkerID, now)
		if err != nil || facts.CancellationRequested || facts.TaskID != initial.TaskID {
			return in, domain.ErrApprovalNotPending
		}
	}
	row, err := q.LockExecutionInteraction(ctx, agentdb.LockExecutionInteractionParams{OwnerUserID: in.OwnerUserID, ID: in.ID})
	if err != nil {
		return in, domain.ErrNotFound
	}
	stored, err := interaction(row)
	if err != nil {
		return in, err
	}
	if stored.State != "pending" {
		if stored.DecisionKey == in.DecisionKey && stored.DecisionDigest == in.DecisionDigest {
			return stored, tx.Commit(ctx)
		}
		return stored, domain.ErrApprovalNotPending
	}
	if !now.Before(stored.ExpiresAt) {
		return stored, domain.ErrApprovalNotPending
	}
	answers, err := json.Marshal(in.Answers)
	if err != nil {
		return stored, err
	}
	if err := q.DecideExecutionInteraction(ctx, agentdb.DecideExecutionInteractionParams{OwnerUserID: in.OwnerUserID, ID: in.ID, State: in.State, Answers: answers, DecisionKey: in.DecisionKey, DecisionDigest: in.DecisionDigest}); err != nil {
		return stored, err
	}
	stored.State = in.State
	stored.Answers = in.Answers
	stored.DecisionKey = in.DecisionKey
	stored.DecisionDigest = in.DecisionDigest
	return stored, tx.Commit(ctx)
}
