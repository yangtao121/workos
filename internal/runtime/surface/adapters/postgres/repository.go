// Package postgres adapts the Surface Broker's storage port to
// runtime-host-owned PostgreSQL tables. The tables reference no Core schema;
// same-key races are arbitrated by the request-mapping primary key.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres/surfacedb"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *surfacedb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: surfacedb.New(pool)}
}

// Create persists the session and its request mapping in one transaction. An
// already-consumed key rules first; a concurrent winner forces the loser to
// re-read and classify; the rolled-back loser leaves no orphan rows.
func (r *Repository) Create(ctx context.Context, command ports.CreateSessionCommand) (domain.SurfaceSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SurfaceSession{}, storeError("begin create surface session", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	request, err := queries.GetSessionRequest(ctx, surfacedb.GetSessionRequestParams{
		OwnerUserID: command.Session.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	if err == nil {
		if request.RequestDigest != command.RequestDigest {
			return domain.SurfaceSession{}, domain.ErrIdempotencyConflict
		}
		return r.readSession(ctx, queries, command.Session.OwnerUserID, command.Session.DeviceID, request.SessionID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SurfaceSession{}, storeError("query surface session request", err)
	}

	if err := queries.InsertSession(ctx, sessionParams(command.Session, command.BridgeTokenHash)); err != nil {
		return domain.SurfaceSession{}, storeError("insert surface session", err)
	}
	return r.consumeKey(ctx, tx, queries, command)
}

func (r *Repository) consumeKey(ctx context.Context, tx pgx.Tx, queries *surfacedb.Queries, command ports.CreateSessionCommand) (domain.SurfaceSession, error) {
	rows, err := queries.InsertSessionRequest(ctx, surfacedb.InsertSessionRequestParams{
		OwnerUserID: command.Session.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		RequestDigest: command.RequestDigest, SessionID: command.Session.ID,
		CreatedAt: timestamp(command.Session.CreatedAt),
	})
	if err != nil {
		return domain.SurfaceSession{}, storeError("insert surface session request", err)
	}
	if rows > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.SurfaceSession{}, storeError("commit create surface session", err)
		}
		return command.Session, nil
	}
	consumed, err := queries.GetSessionRequest(ctx, surfacedb.GetSessionRequestParams{
		OwnerUserID: command.Session.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	if err != nil {
		return domain.SurfaceSession{}, storeError("classify consumed surface session request", err)
	}
	if consumed.RequestDigest != command.RequestDigest {
		return domain.SurfaceSession{}, domain.ErrIdempotencyConflict
	}
	return r.readSession(ctx, queries, command.Session.OwnerUserID, command.Session.DeviceID, consumed.SessionID)
}

func (r *Repository) LookupRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.StoredSessionRequest, bool, error) {
	request, err := r.queries.GetSessionRequest(ctx, surfacedb.GetSessionRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.StoredSessionRequest{}, false, nil
		}
		return ports.StoredSessionRequest{}, false, storeError("query surface session request", err)
	}
	return ports.StoredSessionRequest{RequestDigest: request.RequestDigest, SessionID: request.SessionID}, true, nil
}

func (r *Repository) GetSession(ctx context.Context, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	return r.readSession(ctx, r.queries, ownerUserID, deviceID, sessionID)
}

func (r *Repository) GetActiveSession(ctx context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
	row, err := r.queries.GetActiveSession(ctx, surfacedb.GetActiveSessionParams{
		OwnerUserID: ownerUserID, DeviceID: deviceID, ID: sessionID, Now: timestamp(now),
	})
	if err != nil {
		return domain.SurfaceSession{}, sessionError("query active surface session", err)
	}
	return sessionFromColumns(row.ID, row.OwnerUserID, row.DeviceID, row.IdempotencyKey, row.RequestDigest,
		row.ProjectID, row.AppInstanceID, row.Renderer,
		surfacedbLaunchDescriptor(row), row.Path, row.BridgeTokenHash.String, row.BridgeCapabilities,
		row.CreatedAt, row.ExpiresAt, row.ClosedAt), nil
}

// Close tombstones on first close and atomically clears the bridge token so
// a closed session can never regain a working credential; a repeated close by
// the same owner/device returns the stored session unchanged; anything else
// is NotFound.
func (r *Repository) Close(ctx context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SurfaceSession{}, storeError("begin close surface session", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	if _, err := queries.CloseSession(ctx, surfacedb.CloseSessionParams{
		OwnerUserID: ownerUserID, DeviceID: deviceID, ID: sessionID, Now: timestamp(now),
	}); err != nil {
		return domain.SurfaceSession{}, sessionError("close surface session", err)
	}
	if _, err := queries.ClearSessionBridgeToken(ctx, surfacedb.ClearSessionBridgeTokenParams{
		OwnerUserID: ownerUserID, DeviceID: deviceID, ID: sessionID,
	}); err != nil {
		return domain.SurfaceSession{}, storeError("clear surface bridge token", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SurfaceSession{}, storeError("commit close surface session", err)
	}
	return r.readSession(ctx, r.queries, ownerUserID, deviceID, sessionID)
}

// RotateBridgeToken stores the digest of one freshly minted bridge token on
// the open, unexpired, owner/device-bound session, invalidating the previous
// credential. Anything else is NotFound and nothing changes.
func (r *Repository) RotateBridgeToken(ctx context.Context, command ports.RotateBridgeTokenCommand) error {
	rows, err := r.queries.RotateSessionBridgeToken(ctx, surfacedb.RotateSessionBridgeTokenParams{
		OwnerUserID: command.OwnerUserID, DeviceID: command.DeviceID, SessionID: command.SessionID,
		TokenHash: pgtype.Text{String: command.TokenHash, Valid: true}, Now: timestamp(command.Now),
	})
	if err != nil {
		return storeError("rotate surface bridge token", err)
	}
	if rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// GetActiveSessionByBridgeToken resolves the open, unexpired session that
// currently carries the token digest, owner-scoped.
func (r *Repository) GetActiveSessionByBridgeToken(ctx context.Context, ownerUserID, tokenHash string, now time.Time) (domain.SurfaceSession, error) {
	row, err := r.queries.GetActiveSessionByBridgeToken(ctx, surfacedb.GetActiveSessionByBridgeTokenParams{
		OwnerUserID: ownerUserID, TokenHash: pgtype.Text{String: tokenHash, Valid: true}, Now: timestamp(now),
	})
	if err != nil {
		return domain.SurfaceSession{}, sessionError("query surface session by bridge token", err)
	}
	return sessionFromColumns(row.ID, row.OwnerUserID, row.DeviceID, row.IdempotencyKey, row.RequestDigest,
		row.ProjectID, row.AppInstanceID, row.Renderer,
		surfacedbLaunchDescriptor(row), row.Path, row.BridgeTokenHash.String, row.BridgeCapabilities,
		row.CreatedAt, row.ExpiresAt, row.ClosedAt), nil
}

func (r *Repository) readSession(ctx context.Context, queries *surfacedb.Queries, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	row, err := queries.GetSession(ctx, surfacedb.GetSessionParams{
		OwnerUserID: ownerUserID, DeviceID: deviceID, ID: sessionID,
	})
	if err != nil {
		return domain.SurfaceSession{}, sessionError("query surface session", err)
	}
	return sessionFromColumns(row.ID, row.OwnerUserID, row.DeviceID, row.IdempotencyKey, row.RequestDigest,
		row.ProjectID, row.AppInstanceID, row.Renderer,
		surfacedbLaunchDescriptor(row), row.Path, row.BridgeTokenHash.String, row.BridgeCapabilities,
		row.CreatedAt, row.ExpiresAt, row.ClosedAt), nil
}

func sessionParams(session domain.SurfaceSession, bridgeTokenHash string) surfacedb.InsertSessionParams {
	return surfacedb.InsertSessionParams{
		ID: session.ID, OwnerUserID: session.OwnerUserID, DeviceID: session.DeviceID,
		IdempotencyKey: session.IdempotencyKey, RequestDigest: session.RequestDigest,
		ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID,
		Renderer: session.Renderer, AppID: session.Descriptor.AppID,
		AppVersion: session.Descriptor.Version, ManifestDigest: session.Descriptor.ManifestDigest,
		ArtifactID: session.Descriptor.ArtifactID, ArtifactDigest: session.Descriptor.ArtifactDigest,
		Entrypoint: session.Descriptor.Entrypoint, Path: session.Path,
		BridgeTokenHash:    tokenHashParam(bridgeTokenHash),
		BridgeCapabilities: nonNilCapabilities(session.BridgeCapabilities),
		CreatedAt:          timestamp(session.CreatedAt), ExpiresAt: timestamp(session.ExpiresAt),
	}
}

// surfacedbLaunchDescriptor mirrors the descriptor columns of any session row
// shape (the sqlc Row structs are field-identical).
func surfacedbLaunchDescriptor(row any) domain.LaunchDescriptor {
	switch value := row.(type) {
	case surfacedb.GetSessionRow:
		return domain.LaunchDescriptor{AppID: value.AppID, Version: value.AppVersion, ManifestDigest: value.ManifestDigest, ArtifactID: value.ArtifactID, ArtifactDigest: value.ArtifactDigest, Entrypoint: value.Entrypoint}
	case surfacedb.GetActiveSessionRow:
		return domain.LaunchDescriptor{AppID: value.AppID, Version: value.AppVersion, ManifestDigest: value.ManifestDigest, ArtifactID: value.ArtifactID, ArtifactDigest: value.ArtifactDigest, Entrypoint: value.Entrypoint}
	case surfacedb.GetActiveSessionByBridgeTokenRow:
		return domain.LaunchDescriptor{AppID: value.AppID, Version: value.AppVersion, ManifestDigest: value.ManifestDigest, ArtifactID: value.ArtifactID, ArtifactDigest: value.ArtifactDigest, Entrypoint: value.Entrypoint}
	default:
		return domain.LaunchDescriptor{}
	}
}

func sessionFromColumns(
	id, ownerUserID, deviceID, idempotencyKey, requestDigest string,
	projectID, appInstanceID, renderer string,
	descriptor domain.LaunchDescriptor, path string,
	bridgeTokenHash string, bridgeCapabilities []string,
	createdAt, expiresAt pgtype.Timestamptz, closedAt pgtype.Timestamptz,
) domain.SurfaceSession {
	session := domain.SurfaceSession{
		ID: id, OwnerUserID: ownerUserID, DeviceID: deviceID,
		IdempotencyKey: idempotencyKey, RequestDigest: requestDigest,
		ProjectID: projectID, AppInstanceID: appInstanceID,
		Renderer:           renderer,
		Descriptor:         descriptor,
		Path:               path,
		BridgeTokenHash:    bridgeTokenHash,
		BridgeCapabilities: bridgeCapabilities,
		CreatedAt:          createdAt.Time, ExpiresAt: expiresAt.Time,
	}
	if closedAt.Valid {
		closed := closedAt.Time
		session.ClosedAt = &closed
	}
	return session
}

// tokenHashParam maps an empty hash to SQL NULL ("no currently valid
// credential") and keeps real digests intact.
func tokenHashParam(tokenHash string) pgtype.Text {
	return pgtype.Text{String: tokenHash, Valid: tokenHash != ""}
}

// nonNilCapabilities maps a nil slice to the empty grant so the NOT NULL
// array column never receives SQL NULL.
func nonNilCapabilities(capabilities []string) []string {
	if capabilities == nil {
		return []string{}
	}
	return capabilities
}

func sessionError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return storeError(operation, err)
}

// storeError wraps a storage failure at the port boundary. Transient
// dependency failures (unreachable server, broken connection, resource
// exhaustion) carry the ErrStoreUnavailable sentinel so transports can
// answer a sanitized Unavailable; every other failure stays an opaque
// internal error — classification never reads SQLSTATE message text or
// constraint names.
func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
