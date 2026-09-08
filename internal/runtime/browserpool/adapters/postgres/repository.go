// Package postgres persists the Remote Browser Pool sessions (ADR-0027).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/runtime/browserpool/adapters/postgres/browserpooldb"
	"github.com/yangtao121/workos/internal/runtime/browserpool/domain"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *browserpooldb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: browserpooldb.New(pool)}
}

func transient(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return domain.ErrStoreUnavailable
}

func (r *Repository) InsertSession(ctx context.Context, session domain.Session) (string, bool, error) {
	inserted, err := r.queries.InsertBrowserSession(ctx, browserpooldb.InsertBrowserSessionParams{
		SessionID: session.SessionID, OwnerUserID: session.OwnerUserID, ProjectID: session.ProjectID,
		IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
		CurrentUrl: session.CurrentURL, CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt,
	})
	if err != nil {
		return "", false, transient(err)
	}
	if inserted == 0 {
		stored, err := r.queries.GetBrowserSessionByKey(ctx, browserpooldb.GetBrowserSessionByKeyParams{
			OwnerUserID: session.OwnerUserID, IdempotencyKey: session.IdempotencyKey,
		})
		if err != nil {
			return "", false, transient(err)
		}
		return stored.RequestDigest, false, nil
	}
	return session.RequestDigest, true, nil
}

func sessionFromRow(row browserpooldb.WorkosRuntimeBrowserSession) domain.Session {
	return domain.Session{
		SessionID: row.SessionID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest,
		State: domain.State(row.State), CurrentURL: row.CurrentUrl,
		RestartCount: row.RestartCount, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		ExpiresAt: row.ExpiresAt,
	}
}

func (r *Repository) GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	row, err := r.queries.GetBrowserSession(ctx, browserpooldb.GetBrowserSessionParams{OwnerUserID: ownerUserID, SessionID: sessionID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, domain.ErrNotFound
		}
		return domain.Session{}, transient(err)
	}
	return sessionFromRow(row), nil
}

func (r *Repository) GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error) {
	row, err := r.queries.GetBrowserSessionByKey(ctx, browserpooldb.GetBrowserSessionByKeyParams{
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

func (r *Repository) UpdateRunning(ctx context.Context, ownerUserID, sessionID string, state domain.State, currentURL string, restartCount int32, now time.Time) error {
	updated, err := r.queries.UpdateBrowserSessionRunning(ctx, browserpooldb.UpdateBrowserSessionRunningParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, State: string(state),
		CurrentUrl: currentURL, RestartCount: restartCount, UpdatedAt: now,
	})
	if err != nil {
		return transient(err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *Repository) CloseSession(ctx context.Context, ownerUserID, sessionID string, state domain.State, restartCount int32, now time.Time) error {
	if _, err := r.queries.CloseBrowserSession(ctx, browserpooldb.CloseBrowserSessionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, State: string(state),
		RestartCount: restartCount, UpdatedAt: now,
	}); err != nil {
		return transient(err)
	}
	return nil
}

func (r *Repository) ListActive(ctx context.Context, limit int) ([]domain.Session, error) {
	// The reconcile index drives expiry; enumeration stays bounded.
	rows, err := r.pool.Query(ctx, `
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, current_url, restart_count, created_at, updated_at, expires_at
FROM workos_runtime.browser_sessions
WHERE state IN ('queued', 'running', 'restarting')
ORDER BY updated_at
LIMIT $1`, limit)
	if err != nil {
		return nil, transient(err)
	}
	defer rows.Close()
	sessions := []domain.Session{}
	for rows.Next() {
		var row browserpooldb.WorkosRuntimeBrowserSession
		if err := rows.Scan(&row.SessionID, &row.OwnerUserID, &row.ProjectID, &row.IdempotencyKey, &row.RequestDigest,
			&row.State, &row.CurrentUrl, &row.RestartCount, &row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt); err != nil {
			return nil, transient(err)
		}
		sessions = append(sessions, sessionFromRow(row))
	}
	return sessions, rows.Err()
}

func (r *Repository) ExpireIdle(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := r.queries.ExpireIdleBrowserSessions(ctx, now)
	if err != nil {
		return nil, transient(err)
	}
	return rows, nil
}

func (r *Repository) CountActive(ctx context.Context, ownerUserID string) (int, error) {
	count, err := r.queries.CountActiveBrowserSessions(ctx, ownerUserID)
	if err != nil {
		return 0, transient(err)
	}
	return int(count), nil
}
