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
		SessionID: row.SessionID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
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
