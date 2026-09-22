// Package postgres persists the supervised virtual-display native sessions
// (ADR-0029).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/runtime/nativehost/adapters/postgres/nativehostdb"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *nativehostdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: nativehostdb.New(pool)}
}

func transient(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return domain.ErrStoreUnavailable
}

func (r *Repository) InsertSession(ctx context.Context, session domain.Session) (string, bool, error) {
	mode, err := domain.NormalizeLifecycle(session.LifecycleMode)
	if err != nil {
		return "", false, err
	}
	session.LifecycleMode = mode
	inserted, err := r.queries.InsertNativeSession(ctx, nativehostdb.InsertNativeSessionParams{
		SessionID: session.SessionID, OwnerUserID: session.OwnerUserID, ProjectID: session.ProjectID,
		IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
		Width: int32(session.Width), Height: int32(session.Height),
		CreatedAt: session.CreatedAt, ExpiresAt: optionalTime(session.ExpiresAt), LifecycleMode: int16(session.LifecycleMode),
	})
	if err != nil {
		return "", false, transient(err)
	}
	if inserted == 0 {
		stored, err := r.queries.GetNativeSessionByKey(ctx, nativehostdb.GetNativeSessionByKeyParams{
			OwnerUserID: session.OwnerUserID, IdempotencyKey: session.IdempotencyKey,
		})
		if err != nil {
			return "", false, transient(err)
		}
		return stored.RequestDigest, false, nil
	}
	return session.RequestDigest, true, nil
}

func sessionFromRow(row nativehostdb.WorkosRuntimeNativeSession) domain.Session {
	return domain.Session{
		Generation: row.Generation, SessionID: row.SessionID, OwnerUserID: row.OwnerUserID, ProjectID: row.ProjectID,
		IdempotencyKey: row.IdempotencyKey, RequestDigest: row.RequestDigest,
		State: domain.State(row.State), Width: int32(row.Width), Height: int32(row.Height),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExpiresAt: timeValue(row.ExpiresAt), LifecycleMode: domain.LifecycleMode(row.LifecycleMode),
	}
}

func (r *Repository) GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	row, err := r.queries.GetNativeSession(ctx, nativehostdb.GetNativeSessionParams{OwnerUserID: ownerUserID, SessionID: sessionID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Session{}, domain.ErrNotFound
		}
		return domain.Session{}, transient(err)
	}
	return sessionFromRow(row), nil
}

func (r *Repository) GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error) {
	row, err := r.queries.GetNativeSessionByKey(ctx, nativehostdb.GetNativeSessionByKeyParams{
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
	rows, err := r.queries.ListProjectNativeSessions(ctx, nativehostdb.ListProjectNativeSessionsParams{
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

func (r *Repository) UpdateState(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error {
	updated, err := r.queries.UpdateNativeSessionState(ctx, nativehostdb.UpdateNativeSessionStateParams{
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
	if _, err := r.queries.CloseNativeSession(ctx, nativehostdb.CloseNativeSessionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, State: string(state), UpdatedAt: now,
	}); err != nil {
		return transient(err)
	}
	return nil
}

func (r *Repository) ExpireIdle(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := r.queries.ExpireIdleNativeSessions(ctx, now)
	if err != nil {
		return nil, transient(err)
	}
	return rows, nil
}

func (r *Repository) CountActive(ctx context.Context, ownerUserID string) (int, error) {
	count, err := r.queries.CountActiveNativeSessions(ctx, ownerUserID)
	if err != nil {
		return 0, transient(err)
	}
	return int(count), nil
}

func (r *Repository) ListActive(ctx context.Context) ([]domain.Session, error) {
	rows, err := r.queries.ListActiveNativeSessions(ctx)
	if err != nil {
		return nil, transient(err)
	}
	sessions := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, sessionFromRow(row))
	}
	return sessions, nil
}

func (r *Repository) BeginRestart(ctx context.Context, owner, id, key string, now time.Time, modes ...domain.LifecycleMode) (int64, bool, error) {
	mode, err := domain.NormalizeLifecycle(modes...)
	if err != nil {
		return 0, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	if _, err := q.LockNativeRestart(ctx, nativehostdb.LockNativeRestartParams{OwnerUserID: owner, SessionID: id}); err != nil {
		return 0, false, domain.ErrNotFound
	}
	previous, err := q.GetNativeRestartReceipt(ctx, nativehostdb.GetNativeRestartReceiptParams{SessionID: id, ActionKey: key})
	if err == nil {
		if previous.LifecycleMode != int16(mode) {
			return 0, false, domain.ErrIdempotencyDrift
		}
		return previous.Generation, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	generation, err := q.BeginNativeRestart(ctx, nativehostdb.BeginNativeRestartParams{OwnerUserID: owner, SessionID: id, UpdatedAt: now, ExpiresAt: optionalTime(mode.Expiry(now)), LifecycleMode: int16(mode)})
	if err != nil {
		return 0, false, err
	}
	if err := q.RecordNativeRestart(ctx, nativehostdb.RecordNativeRestartParams{SessionID: id, ActionKey: key, Generation: generation, LifecycleMode: int16(mode)}); err != nil {
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
	generation, err := q.LockNativeRestart(ctx, nativehostdb.LockNativeRestartParams{OwnerUserID: owner, SessionID: id})
	if err != nil {
		return false, domain.ErrNotFound
	}
	_, err = q.GetNativeStopReceipt(ctx, nativehostdb.GetNativeStopReceiptParams{SessionID: id, ActionKey: key})
	if err == nil {
		return false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err = q.RecordNativeStop(ctx, nativehostdb.RecordNativeStopParams{SessionID: id, ActionKey: key, Generation: generation}); err != nil {
		return false, err
	}
	_, err = q.CloseNativeSession(ctx, nativehostdb.CloseNativeSessionParams{OwnerUserID: owner, SessionID: id, State: string(domain.StateClosed), UpdatedAt: now})
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
