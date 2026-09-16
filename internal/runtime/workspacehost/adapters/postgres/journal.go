package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/postgres/workspacedb"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
)

type Journal struct{ queries *workspacedb.Queries }

func New(pool *pgxpool.Pool) *Journal { return &Journal{workspacedb.New(pool)} }
func digest(op domain.Operation) string {
	data, _ := json.Marshal(op)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (j *Journal) Begin(ctx context.Context, op domain.Operation) (domain.Result, bool, error) {
	hash := digest(op)
	count, err := j.queries.BeginWorkspaceOperation(ctx, workspacedb.BeginWorkspaceOperationParams{OperationID: op.ID, OwnerUserID: op.OwnerUserID, ProjectID: op.ProjectID, RequestDigest: hash})
	if err != nil {
		return nil, false, err
	}
	if count == 1 {
		return nil, true, nil
	}
	saved, err := j.queries.GetWorkspaceOperation(ctx, op.ID)
	if err != nil {
		return nil, false, err
	}
	if saved.OwnerUserID != op.OwnerUserID || saved.ProjectID != op.ProjectID || saved.RequestDigest != hash {
		return nil, false, domain.ErrDenied
	}
	if saved.State != "completed" {
		return nil, false, domain.ErrUnknownOutcome
	}
	var result domain.Result
	if err := json.Unmarshal(saved.Result, &result); err != nil {
		return nil, false, domain.ErrUnavailable
	}
	return result, false, nil
}
func (j *Journal) Complete(ctx context.Context, op domain.Operation, result domain.Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	n, err := j.queries.CompleteWorkspaceOperation(ctx, workspacedb.CompleteWorkspaceOperationParams{OperationID: op.ID, OwnerUserID: op.OwnerUserID, RequestDigest: digest(op), Result: data})
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.ErrUnknownOutcome
	}
	return nil
}
