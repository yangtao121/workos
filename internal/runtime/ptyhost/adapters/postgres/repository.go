// Package postgres persists the supervised PTY sessions (ADR-0028).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/runtime/ptyhost/adapters/postgres/ptyhostdb"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *ptyhostdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: ptyhostdb.New(pool)}
}

func transient(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return domain.ErrStoreUnavailable
}

func (r *Repository) InsertSession(ctx context.Context, session domain.Session) (string, bool, error) {
	inserted, err := r.queries.InsertPtySession(ctx, ptyhostdb.InsertPtySessionParams{
		SessionID: session.SessionID, OwnerUserID: session.OwnerUserID, ProjectID: session.ProjectID,
		IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
		CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt,
	})
	if err != nil {
		return "", false, transient(err)
	}
	if inserted == 0 {
		stored, err := r.queries.GetPtySessionByKey(ctx, ptyhostdb.GetPtySessionByKeyParams{
			OwnerUserID: session.OwnerUserID, IdempotencyKey: session.IdempotencyKey,
		})
		if err != nil {
			return "", false, transient(err)
		}
		return stored.RequestDigest, false, nil
	}
	return session.RequestDigest, true, nil
}

func sessionFromRow(row ptyhostdb.WorkosRuntimePtySession) domain.Session {
	return domain.Session{
		Generation: row.Generation, SessionID: row.SessionID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest,
		State: domain.State(row.State), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		ExpiresAt: row.ExpiresAt,
	}
}

func (r *Repository) GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	row, err := r.queries.GetPtySession(ctx, ptyhostdb.GetPtySessionParams{OwnerUserID: ownerUserID, SessionID: sessionID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, domain.ErrNotFound
		}
		return domain.Session{}, transient(err)
	}
	return sessionFromRow(row), nil
}

func (r *Repository) GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error) {
	row, err := r.queries.GetPtySessionByKey(ctx, ptyhostdb.GetPtySessionByKeyParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, domain.ErrNotFound
		}
		return domain.Session{}, transient(err)
	}
	return sessionFromRow(row), nil
}

// ListProjectSessions returns the owner's non-terminal sessions of one
// project (ADR-0031 discovery view).
func (r *Repository) ListProjectSessions(ctx context.Context, ownerUserID, projectID string) ([]domain.Session, error) {
	rows, err := r.queries.ListProjectPtySessions(ctx, ptyhostdb.ListProjectPtySessionsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID,
	})
	if err != nil {
		return nil, transient(err)
	}
	sessions := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, sessionFromRow(row))
	}
	return sessions, nil
}

// ListActive returns every non-terminal session row across owners: the
// startup reconcile input (a dead process must be finalized, never listed).
func (r *Repository) ListActive(ctx context.Context) ([]domain.Session, error) {
	rows, err := r.queries.ListActivePtySessions(ctx)
	if err != nil {
		return nil, transient(err)
	}
	sessions := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, sessionFromRow(row))
	}
	return sessions, nil
}

func (r *Repository) UpdateState(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error {
	updated, err := r.queries.UpdatePtySessionState(ctx, ptyhostdb.UpdatePtySessionStateParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, State: string(state), UpdatedAt: now,
	})
	if err != nil {
		return transient(err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) CloseSession(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error {
	if _, err := r.queries.ClosePtySession(ctx, ptyhostdb.ClosePtySessionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, State: string(state), UpdatedAt: now,
	}); err != nil {
		return transient(err)
	}
	return nil
}

func (r *Repository) ExpireIdle(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := r.queries.ExpireIdlePtySessions(ctx, now)
	if err != nil {
		return nil, transient(err)
	}
	return rows, nil
}

func (r *Repository) CountActive(ctx context.Context, ownerUserID string) (int, error) {
	count, err := r.queries.CountActivePtySessions(ctx, ownerUserID)
	if err != nil {
		return 0, transient(err)
	}
	return int(count), nil
}

func (r *Repository) BeginRestart(ctx context.Context, owner, id, key string, now time.Time) (int64, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	if _, err := q.LockPtyRestart(ctx, ptyhostdb.LockPtyRestartParams{OwnerUserID: owner, SessionID: id}); err != nil {
		return 0, false, domain.ErrNotFound
	}
	previous, err := q.GetPtyRestartReceipt(ctx, ptyhostdb.GetPtyRestartReceiptParams{SessionID: id, ActionKey: key})
	if err == nil {
		return previous, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	generation, err := q.BeginPtyRestart(ctx, ptyhostdb.BeginPtyRestartParams{OwnerUserID: owner, SessionID: id, UpdatedAt: now, ExpiresAt: now.Add(domain.SessionTTL)})
	if err != nil {
		return 0, false, err
	}
	if err := q.RecordPtyRestart(ctx, ptyhostdb.RecordPtyRestartParams{SessionID: id, ActionKey: key, Generation: generation}); err != nil {
		return 0, false, err
	}
	return generation, true, tx.Commit(ctx)
}

func (r *Repository) BeginStop(ctx context.Context, owner, id, key string, now time.Time) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	generation, err := q.LockPtyRestart(ctx, ptyhostdb.LockPtyRestartParams{OwnerUserID: owner, SessionID: id})
	if err != nil {
		return false, domain.ErrNotFound
	}
	_, err = q.GetPtyStopReceipt(ctx, ptyhostdb.GetPtyStopReceiptParams{SessionID: id, ActionKey: key})
	if err == nil {
		return false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err = q.RecordPtyStop(ctx, ptyhostdb.RecordPtyStopParams{SessionID: id, ActionKey: key, Generation: generation}); err != nil {
		return false, err
	}
	_, err = q.ClosePtySession(ctx, ptyhostdb.ClosePtySessionParams{OwnerUserID: owner, SessionID: id, State: string(domain.StateClosed), UpdatedAt: now})
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
