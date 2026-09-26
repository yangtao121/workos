package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

func (s *Store) Credential(ctx context.Context, ownerID string) (string, string, error) {
	var username, hash string
	err := s.pool.QueryRow(ctx, `SELECT username, password_hash FROM workos_gateway.password_credentials WHERE owner_user_id=$1`, ownerID).Scan(&username, &hash)
	if err != nil {
		return "", "", noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return username, hash, nil
}

func (s *Store) SetPassword(ctx context.Context, ownerID, username, hash string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return s.wrap(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `INSERT INTO workos_gateway.password_credentials(owner_user_id,username,password_hash,revision,updated_at)
		VALUES($1,$2,$3,1,$4) ON CONFLICT(owner_user_id) DO UPDATE SET username=EXCLUDED.username,
		password_hash=EXCLUDED.password_hash,revision=password_credentials.revision+1,updated_at=EXCLUDED.updated_at`, ownerID, username, hash, now)
	if err != nil {
		return s.wrap(err)
	}
	_, err = tx.Exec(ctx, `UPDATE workos_gateway.password_sessions SET revoked_at=$2 WHERE owner_user_id=$1 AND revoked_at IS NULL`, ownerID, now)
	if err != nil {
		return s.wrap(err)
	}
	return s.wrap(tx.Commit(ctx))
}

func (s *Store) CreatePasswordSession(ctx context.Context, ownerID, username, expectedHash string, device domain.Device, session domain.DeviceSession) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return s.wrap(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var currentUser, currentHash string
	err = tx.QueryRow(ctx, `SELECT username,password_hash FROM workos_gateway.password_credentials WHERE owner_user_id=$1 FOR UPDATE`, ownerID).Scan(&currentUser, &currentHash)
	if err != nil {
		return noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	if currentUser != username || currentHash != expectedHash {
		return domain.ErrAuthenticationFailed
	}
	_, err = tx.Exec(ctx, `INSERT INTO workos_gateway.password_devices(id,owner_user_id,name,device_class,revision,created_at,last_authenticated_at)
		VALUES($1,$2,$3,$4,1,$5,$5)`, device.ID, ownerID, device.Name, string(device.Class), device.CreatedAt)
	if err != nil {
		return s.wrap(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO workos_gateway.password_sessions(id,owner_user_id,device_id,token_hash,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6)`, session.ID, ownerID, device.ID, session.TokenHash, session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return s.wrap(err)
	}
	return s.wrap(tx.Commit(ctx))
}

func scanPasswordDevice(row pgx.Row) (domain.Device, error) {
	var d domain.Device
	var class string
	err := row.Scan(&d.ID, &d.OwnerID, &d.Name, &class, &d.Revision, &d.CreatedAt, &d.LastAuthenticatedAt, &d.RevokedAt)
	if err != nil {
		return domain.Device{}, err
	}
	d.Class = domain.DeviceClass(class)
	if !domain.ValidUUIDv7(d.ID) || !domain.ValidUUIDv7(d.OwnerID) || d.Revision < 1 {
		return domain.Device{}, domain.ErrAuthCorrupt
	}
	if _, err := domain.ParseDeviceClass(class); err != nil {
		return domain.Device{}, domain.ErrAuthCorrupt
	}
	return d, nil
}

const passwordDeviceColumns = `id,owner_user_id,name,device_class,revision,created_at,last_authenticated_at,revoked_at`

func (s *Store) ResolvePasswordSession(ctx context.Context, tokenHash string) (domain.DeviceSession, domain.Device, error) {
	var session domain.DeviceSession
	err := s.pool.QueryRow(ctx, `SELECT id,owner_user_id,device_id,token_hash,created_at,expires_at,last_seen_at,revoked_at
		FROM workos_gateway.password_sessions WHERE token_hash=$1`, tokenHash).Scan(
		&session.ID, &session.OwnerID, &session.DeviceID, &session.TokenHash, &session.CreatedAt, &session.ExpiresAt, &session.LastSeenAt, &session.RevokedAt)
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	device, err := s.GetPasswordDevice(ctx, session.OwnerID, session.DeviceID)
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, err
	}
	return session, device, nil
}

func (s *Store) GetPasswordDevice(ctx context.Context, ownerID, deviceID string) (domain.Device, error) {
	d, err := scanPasswordDevice(s.pool.QueryRow(ctx, `SELECT `+passwordDeviceColumns+` FROM workos_gateway.password_devices WHERE owner_user_id=$1 AND id=$2`, ownerID, deviceID))
	if err != nil {
		return domain.Device{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return d, nil
}

func (s *Store) ListPasswordDevices(ctx context.Context, ownerID, cursor string, limit int) ([]domain.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+passwordDeviceColumns+` FROM workos_gateway.password_devices
		WHERE owner_user_id=$1 AND id<$2 ORDER BY id DESC LIMIT $3`, ownerID, cursor, limit)
	if err != nil {
		return nil, s.wrap(err)
	}
	defer rows.Close()
	var result []domain.Device
	for rows.Next() {
		d, err := scanPasswordDevice(rows)
		if err != nil {
			return nil, s.wrap(err)
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, s.wrap(err)
	}
	return result, nil
}

func (s *Store) RevokePasswordDevice(ctx context.Context, op ports.RevokeDeviceOp) (domain.Device, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Serialize attempts using the same owner/key; a duplicate call replays
	// its committed result, while a changed request under that key conflicts.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, op.OwnerID+":"+op.IdempotencyKey)
	if err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	var digest string
	var snapshot []byte
	err = tx.QueryRow(ctx, `SELECT request_digest,result FROM workos_gateway.password_device_revocations
		WHERE owner_user_id=$1 AND idempotency_key=$2`, op.OwnerID, op.IdempotencyKey).Scan(&digest, &snapshot)
	if err == nil {
		if digest != op.RequestDigest {
			return domain.Device{}, false, domain.ErrConflict
		}
		var d domain.Device
		if json.Unmarshal(snapshot, &d) != nil {
			return domain.Device{}, false, domain.ErrAuthCorrupt
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Device{}, false, s.wrap(err)
		}
		return d, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, false, s.wrap(err)
	}
	d, err := scanPasswordDevice(tx.QueryRow(ctx, `SELECT `+passwordDeviceColumns+` FROM workos_gateway.password_devices
		WHERE owner_user_id=$1 AND id=$2 FOR UPDATE`, op.OwnerID, op.DeviceID))
	if err != nil {
		return domain.Device{}, false, noRows(s.wrap(err), domain.ErrDeviceNotFound)
	}
	if d.RevokedAt != nil || d.Revision != op.ExpectedRevision {
		return domain.Device{}, false, domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE workos_gateway.password_devices SET revision=revision+1,revoked_at=$3 WHERE owner_user_id=$1 AND id=$2`, op.OwnerID, op.DeviceID, op.Now)
	if err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	_, err = tx.Exec(ctx, `UPDATE workos_gateway.password_sessions SET revoked_at=$3 WHERE owner_user_id=$1 AND device_id=$2 AND revoked_at IS NULL`, op.OwnerID, op.DeviceID, op.Now)
	if err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	d.Revision++
	d.RevokedAt = &op.Now
	snapshot, err = json.Marshal(d)
	if err != nil {
		return domain.Device{}, false, domain.ErrAuthCorrupt
	}
	_, err = tx.Exec(ctx, `INSERT INTO workos_gateway.password_device_revocations(owner_user_id,idempotency_key,request_digest,result,created_at)
		VALUES($1,$2,$3,$4,$5)`, op.OwnerID, op.IdempotencyKey, op.RequestDigest, snapshot, op.Now)
	if err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Device{}, false, s.wrap(err)
	}
	return d, false, nil
}

func (s *Store) LogoutPasswordSession(ctx context.Context, ownerID, sessionID string, now time.Time) error {
	result, err := s.pool.Exec(ctx, `UPDATE workos_gateway.password_sessions SET revoked_at=$3
		WHERE owner_user_id=$1 AND id=$2 AND revoked_at IS NULL`, ownerID, sessionID, now)
	if err != nil {
		return s.wrap(err)
	}
	if result.RowsAffected() == 0 {
		return domain.ErrAuthenticationFailed
	}
	return nil
}
