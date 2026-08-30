// Package postgres implements the Gateway-owned device authentication
// repository on PostgreSQL. Every interface method maps to exactly one
// transaction; guarded UPDATE row counts, row locks, and unique indexes
// decide the verdicts, and secret material only ever appears hashed.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres/gatewayauthdb"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

// Store is the pgx-backed ports.Repository.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ready(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) queries() *gatewayauthdb.Queries {
	return gatewayauthdb.New(s.pool)
}

func (s *Store) tx(ctx context.Context, fn func(q *gatewayauthdb.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return s.wrap(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := gatewayauthdb.New(tx)
	if err := fn(q); err != nil {
		return s.wrap(err)
	}
	return s.wrap(tx.Commit(ctx))
}

// wrap maps driver failures onto the sanitized verdicts. Typed verdict
// errors pass through untouched; "no rows" stays untouched so each call
// site can map it to the right verdict. Unique-index violations on guarded
// inserts are authentication failures (a duplicate active credential key),
// never internal leaks.
func (s *Store) wrap(err error) error {
	if err == nil {
		return nil
	}
	if domain.IsVerdict(err) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: duplicate credential", domain.ErrAuthenticationFailed)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return domain.ErrStoreUnavailable
	}
	return fmt.Errorf("gateway auth store: %w", domain.ErrStoreUnavailable)
}

// noRows maps a driver "no rows" error to the given sanitized verdict.
func noRows(err error, verdict error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return verdict
	}
	return err
}

func (s *Store) RotatePairingTicket(ctx context.Context, ticket domain.PairingTicket) error {
	return s.tx(ctx, func(q *gatewayauthdb.Queries) error {
		// The owner-level advisory lock serializes rotations: the loser
		// waits, then revokes the winner's pending ticket before inserting
		// its own, so exactly one pending ticket survives per owner.
		if err := q.LockOwnerTicketRotation(ctx, ticket.OwnerID); err != nil {
			return err
		}
		if _, err := q.RevokeOutstandingTickets(ctx, ticket.OwnerID); err != nil {
			return err
		}
		return q.InsertPairingTicket(ctx, gatewayauthdb.InsertPairingTicketParams{
			ID:             ticket.ID,
			OwnerUserID:    ticket.OwnerID,
			SecretHash:     ticket.SecretHash,
			PublicOrigin:   ticket.PublicOrigin,
			TlsFingerprint: ticket.TLSFingerprint,
			ExpiresAt:      ticket.ExpiresAt,
			CreatedAt:      ticket.CreatedAt,
		})
	})
}

func (s *Store) LoadTicketBySecretHash(ctx context.Context, secretHash string, now time.Time) (domain.PairingTicket, error) {
	row, err := s.queries().GetTicketBySecretHash(ctx, gatewayauthdb.GetTicketBySecretHashParams{
		SecretHash: secretHash,
		Now:        now,
	})
	if err != nil {
		return domain.PairingTicket{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return ticketFromRow(row)
}

func (s *Store) LoadTicket(ctx context.Context, id, ownerID string) (domain.PairingTicket, error) {
	row, err := s.queries().GetPairingTicket(ctx, gatewayauthdb.GetPairingTicketParams{ID: id, OwnerUserID: ownerID})
	if err != nil {
		return domain.PairingTicket{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return ticketFromRow(row)
}

func (s *Store) ClaimPairingTicket(ctx context.Context, ticketID, ownerID, deviceID, publicKeyHash, deviceName, deviceClass string, now time.Time) (domain.PairingTicket, error) {
	row, err := s.queries().ClaimPairingTicket(ctx, gatewayauthdb.ClaimPairingTicketParams{
		ID:            ticketID,
		OwnerUserID:   ownerID,
		DeviceID:      uuidOrNil(deviceID),
		PublicKeyHash: textOrNil(publicKeyHash),
		ClaimedName:   textOrNil(deviceName),
		ClaimedClass:  textOrNil(deviceClass),
		Now:           now,
	})
	if err != nil {
		return domain.PairingTicket{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return ticketFromRow(row)
}

func (s *Store) FailTicketAttempt(ctx context.Context, ticketID string) error {
	_, err := s.queries().FailTicketAttempt(ctx, ticketID)
	return s.wrap(err)
}

func (s *Store) CreateChallenge(ctx context.Context, challenge domain.Challenge) error {
	return s.wrap(s.queries().InsertChallenge(ctx, gatewayauthdb.InsertChallengeParams{
		ID:            challenge.ID,
		Purpose:       string(challenge.Purpose),
		DeviceID:      uuidOrNil(challenge.DeviceID),
		TicketID:      uuidOrNil(challenge.TicketID),
		PublicKeyHash: challenge.PublicKeyHash,
		Nonce:         challenge.Nonce,
		ExpiresAt:     challenge.ExpiresAt,
		CreatedAt:     challenge.CreatedAt,
	}))
}

func (s *Store) LoadChallenge(ctx context.Context, id string) (domain.Challenge, error) {
	row, err := s.queries().GetChallenge(ctx, id)
	if err != nil {
		return domain.Challenge{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	return challengeFromRow(row)
}

func (s *Store) ConsumeChallenge(ctx context.Context, id, deviceID string, result domain.ChallengeResult, now time.Time) error {
	rows, err := s.queries().ConsumeChallenge(ctx, gatewayauthdb.ConsumeChallengeParams{
		ID:       id,
		DeviceID: uuidOrNil(deviceID),
		Result:   string(result),
		Now:      now,
	})
	if err != nil {
		return s.wrap(err)
	}
	if rows == 0 {
		return domain.ErrAuthenticationFailed
	}
	return nil
}

func (s *Store) FailChallengeAttempt(ctx context.Context, id string) error {
	_, err := s.queries().FailChallengeAttempt(ctx, id)
	return s.wrap(err)
}

func (s *Store) LoadActiveDevice(ctx context.Context, id string) (domain.Device, error) {
	row, err := s.queries().GetActiveDeviceByID(ctx, id)
	if err != nil {
		return domain.Device{}, noRows(s.wrap(err), domain.ErrDeviceNotFound)
	}
	return deviceFromRow(row)
}

func (s *Store) CompletePairing(ctx context.Context, op ports.CompletePairingOp) (domain.Device, domain.DeviceSession, error) {
	var device domain.Device
	var session domain.DeviceSession
	err := s.tx(ctx, func(q *gatewayauthdb.Queries) error {
		ticketRow, err := q.LockTicketForComplete(ctx, op.TicketID)
		if err != nil {
			return errAuthentication(err)
		}
		ticket, err := ticketFromRow(ticketRow)
		if err != nil {
			return err
		}
		if ticket.DeviceID != op.DeviceID || ticket.PublicKeyHash != op.PublicKeyHash ||
			ticket.OwnerID != op.OwnerID || !ticket.Recoverable(op.Now) {
			return domain.ErrAuthenticationFailed
		}
		challengeRow, err := q.LockChallenge(ctx, op.ChallengeID)
		if err != nil {
			return errAuthentication(err)
		}
		challenge, err := challengeFromRow(challengeRow)
		if err != nil {
			return err
		}
		if err := validatePairingChallenge(challenge, op); err != nil {
			return err
		}
		if err := q.InsertDevice(ctx, gatewayauthdb.InsertDeviceParams{
			ID:            op.DeviceID,
			OwnerUserID:   op.OwnerID,
			Name:          op.DeviceName,
			DeviceClass:   op.DeviceClass,
			PublicKeySpki: op.PublicKeySPKI,
			PublicKeyHash: op.PublicKeyHash,
			Now:           op.Now,
		}); err != nil {
			return err
		}
		completed, err := q.CompletePairingTicket(ctx, gatewayauthdb.CompletePairingTicketParams{ID: op.TicketID, Now: op.Now})
		if err != nil {
			return err
		}
		if completed == 0 {
			return domain.ErrAuthenticationFailed
		}
		consumed, err := q.ConsumeChallenge(ctx, gatewayauthdb.ConsumeChallengeParams{
			ID:       op.ChallengeID,
			DeviceID: uuidOrNil(op.DeviceID),
			Result:   string(domain.ChallengeVerified),
			Now:      op.Now,
		})
		if err != nil {
			return err
		}
		if consumed == 0 {
			return domain.ErrAuthenticationFailed
		}
		if _, err := q.RevokeActiveSessions(ctx, gatewayauthdb.RevokeActiveSessionsParams{
			DeviceID:    op.DeviceID,
			OwnerUserID: op.OwnerID,
		}); err != nil {
			return err
		}
		sessionID, expiresAt, err := insertSession(ctx, q, op.OwnerID, op.DeviceID, op.SessionID, op.SessionTokenHash, op.Now, op.SessionExpiresAt)
		if err != nil {
			return err
		}
		if _, err := q.TouchDeviceAuthenticated(ctx, gatewayauthdb.TouchDeviceAuthenticatedParams{
			ID:          op.DeviceID,
			OwnerUserID: op.OwnerID,
			Now:         op.Now,
		}); err != nil {
			return err
		}
		device = domain.Device{
			ID: op.DeviceID, OwnerID: op.OwnerID, Name: op.DeviceName,
			Class: domain.DeviceClass(op.DeviceClass), PublicKeySPKI: op.PublicKeySPKI,
			PublicKeyHash: op.PublicKeyHash, Revision: 1,
			CreatedAt: op.Now, LastAuthenticatedAt: op.Now,
		}
		session = domain.DeviceSession{
			ID: sessionID, OwnerID: op.OwnerID, DeviceID: op.DeviceID,
			CreatedAt: op.Now, ExpiresAt: expiresAt,
		}
		return nil
	})
	if err != nil {
		return domain.Device{}, domain.DeviceSession{}, err
	}
	return device, session, nil
}

func (s *Store) CompleteSession(ctx context.Context, op ports.CompleteSessionOp) (domain.Device, domain.DeviceSession, error) {
	var device domain.Device
	var session domain.DeviceSession
	err := s.tx(ctx, func(q *gatewayauthdb.Queries) error {
		deviceRow, err := q.LockDeviceByID(ctx, op.DeviceID)
		if err != nil {
			return errAuthentication(err)
		}
		device, err = deviceFromRow(deviceRow)
		if err != nil {
			return err
		}
		challengeRow, err := q.LockChallenge(ctx, op.ChallengeID)
		if err != nil {
			return errAuthentication(err)
		}
		challenge, err := challengeFromRow(challengeRow)
		if err != nil {
			return err
		}
		if challenge.Purpose != domain.ChallengeSession || challenge.DeviceID != op.DeviceID ||
			challenge.PublicKeyHash != device.PublicKeyHash || !challenge.Usable(op.Now) {
			return domain.ErrAuthenticationFailed
		}
		consumed, err := q.ConsumeChallenge(ctx, gatewayauthdb.ConsumeChallengeParams{
			ID:       op.ChallengeID,
			DeviceID: uuidOrNil(op.DeviceID),
			Result:   string(domain.ChallengeVerified),
			Now:      op.Now,
		})
		if err != nil {
			return err
		}
		if consumed == 0 {
			return domain.ErrAuthenticationFailed
		}
		if _, err := q.RevokeActiveSessions(ctx, gatewayauthdb.RevokeActiveSessionsParams{
			DeviceID:    op.DeviceID,
			OwnerUserID: device.OwnerID,
		}); err != nil {
			return err
		}
		sessionID, expiresAt, err := insertSession(ctx, q, device.OwnerID, op.DeviceID, op.SessionID, op.SessionTokenHash, op.Now, op.SessionExpiresAt)
		if err != nil {
			return err
		}
		if _, err := q.TouchDeviceAuthenticated(ctx, gatewayauthdb.TouchDeviceAuthenticatedParams{
			ID:          op.DeviceID,
			OwnerUserID: device.OwnerID,
			Now:         op.Now,
		}); err != nil {
			return err
		}
		device.LastAuthenticatedAt = op.Now
		session = domain.DeviceSession{
			ID: sessionID, OwnerID: device.OwnerID, DeviceID: op.DeviceID,
			CreatedAt: op.Now, ExpiresAt: expiresAt,
		}
		return nil
	})
	if err != nil {
		return domain.Device{}, domain.DeviceSession{}, err
	}
	return device, session, nil
}

// validatePairingChallenge re-checks the challenge binding inside the
// completion transaction, against the locked rows.
func validatePairingChallenge(challenge domain.Challenge, op ports.CompletePairingOp) error {
	if challenge.Purpose != domain.ChallengePairing || challenge.DeviceID != op.DeviceID ||
		challenge.TicketID != op.TicketID || challenge.PublicKeyHash != op.PublicKeyHash ||
		!challenge.Usable(op.Now) {
		return domain.ErrAuthenticationFailed
	}
	return nil
}

func insertSession(ctx context.Context, q *gatewayauthdb.Queries, ownerID, deviceID, sessionID, tokenHash string, now, expiresAt time.Time) (string, time.Time, error) {
	if err := q.InsertSession(ctx, gatewayauthdb.InsertSessionParams{
		ID: sessionID, OwnerUserID: ownerID, DeviceID: deviceID,
		TokenHash: tokenHash, CreatedAt: now, ExpiresAt: expiresAt,
	}); err != nil {
		return "", time.Time{}, err
	}
	return sessionID, expiresAt, nil
}

func (s *Store) ResolveSession(ctx context.Context, tokenHash string) (domain.DeviceSession, domain.Device, error) {
	sessionRow, err := s.queries().GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	session, err := sessionFromRow(sessionRow)
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, err
	}
	deviceRow, err := s.queries().GetDevice(ctx, gatewayauthdb.GetDeviceParams{
		ID: session.DeviceID, OwnerUserID: session.OwnerID,
	})
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, noRows(s.wrap(err), domain.ErrAuthenticationFailed)
	}
	device, err := deviceFromRow(deviceRow)
	if err != nil {
		return domain.DeviceSession{}, domain.Device{}, err
	}
	return session, device, nil
}

func (s *Store) TouchSessionLastSeen(ctx context.Context, sessionID string, now, threshold time.Time) {
	_, _ = s.queries().TouchSessionLastSeen(ctx, gatewayauthdb.TouchSessionLastSeenParams{
		ID: sessionID, Now: now, Threshold: threshold,
	})
}

func (s *Store) ListDevices(ctx context.Context, ownerID, cursorUUID string, limit int) ([]domain.Device, error) {
	rows, err := s.queries().ListDevices(ctx, gatewayauthdb.ListDevicesParams{
		OwnerUserID: ownerID,
		Cursor:      cursorUUID,
		RowLimit:    int32(limit),
	})
	if err != nil {
		return nil, s.wrap(err)
	}
	devices := make([]domain.Device, 0, len(rows))
	for _, row := range rows {
		device, parseErr := deviceFromRow(row)
		if parseErr != nil {
			return nil, parseErr
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (s *Store) RevokeDevice(ctx context.Context, op ports.RevokeDeviceOp) (domain.Device, bool, error) {
	var device domain.Device
	replayed := false
	err := s.tx(ctx, func(q *gatewayauthdb.Queries) error {
		// Serialize duplicates of one idempotency key: the second request
		// waits here for the first transaction to commit and then replays
		// its persisted snapshot instead of racing it (a racing duplicate
		// would see neither request row nor a revocable device).
		if err := q.LockRevocationKey(ctx, op.OwnerID+"|"+op.IdempotencyKey); err != nil {
			return err
		}
		existing, err := q.GetRevocationRequest(ctx, gatewayauthdb.GetRevocationRequestParams{
			OwnerUserID: op.OwnerID, IdempotencyKey: op.IdempotencyKey,
		})
		switch {
		case err == nil:
			if existing.ResultVersion != "v1" || existing.OwnerUserID != op.OwnerID ||
				existing.IdempotencyKey != op.IdempotencyKey || !domain.ValidUUIDv7(existing.OwnerUserID) ||
				!domain.ValidUUID(existing.IdempotencyKey) || !domain.ValidDigest(existing.RequestDigest) ||
				existing.CreatedAt.IsZero() {
				return corruptStored("revocation request")
			}
			snapshot, parseErr := domain.ParseRevocationSnapshot(existing.Result)
			if parseErr != nil {
				return parseErr
			}
			if existing.RequestDigest != op.RequestDigest {
				return domain.ErrConflict
			}
			createdAt, _ := time.Parse(time.RFC3339Nano, snapshot.CreatedAt)
			lastAuthenticatedAt, _ := time.Parse(time.RFC3339Nano, snapshot.LastAuthenticatedAt)
			revokedAt, _ := time.Parse(time.RFC3339Nano, snapshot.RevokedAt)
			if snapshot.DeviceID != op.DeviceID || snapshot.Revision != op.ExpectedRevision+1 ||
				!existing.CreatedAt.Equal(revokedAt.Truncate(time.Microsecond)) {
				return corruptStored("revocation request binding")
			}
			device = domain.Device{
				ID: snapshot.DeviceID, OwnerID: op.OwnerID, Name: snapshot.Name,
				Class: domain.DeviceClass(snapshot.Class), Revision: snapshot.Revision,
				CreatedAt: createdAt, LastAuthenticatedAt: lastAuthenticatedAt, RevokedAt: &revokedAt,
			}
			replayed = true
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		deviceRow, err := q.LockDeviceByID(ctx, op.DeviceID)
		if err != nil {
			return errDeviceNotFound(err)
		}
		current, err := deviceFromRow(deviceRow)
		if err != nil {
			return err
		}
		if current.OwnerID != op.OwnerID {
			return domain.ErrDeviceNotFound
		}
		if current.Revision != op.ExpectedRevision {
			return domain.ErrConflict
		}
		rows, err := q.RevokeDeviceCredential(ctx, gatewayauthdb.RevokeDeviceCredentialParams{
			ID: op.DeviceID, OwnerUserID: op.OwnerID,
			ExpectedRevision: op.ExpectedRevision, Now: op.Now,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return domain.ErrConflict
		}
		if _, err := q.RevokeActiveSessions(ctx, gatewayauthdb.RevokeActiveSessionsParams{
			DeviceID: op.DeviceID, OwnerUserID: op.OwnerID,
		}); err != nil {
			return err
		}
		current.Revision = op.ExpectedRevision + 1
		current.RevokedAt = &op.Now
		snapshot := domain.RevocationSnapshot{
			ResultVersion: "v1", DeviceID: current.ID, Name: current.Name,
			Class: string(current.Class), Revision: current.Revision,
			CreatedAt:           current.CreatedAt.UTC().Format(time.RFC3339Nano),
			LastAuthenticatedAt: current.LastAuthenticatedAt.UTC().Format(time.RFC3339Nano),
			RevokedAt:           op.Now.UTC().Format(time.RFC3339Nano),
		}
		encoded, marshalErr := marshalSnapshot(snapshot)
		if marshalErr != nil {
			return marshalErr
		}
		if err := q.InsertRevocationRequest(ctx, gatewayauthdb.InsertRevocationRequestParams{
			OwnerUserID: op.OwnerID, IdempotencyKey: op.IdempotencyKey,
			RequestDigest: op.RequestDigest, Result: encoded, CreatedAt: op.Now,
		}); err != nil {
			return err
		}
		device = current
		return nil
	})
	if err != nil {
		return domain.Device{}, false, err
	}
	return device, replayed, nil
}

func (s *Store) Logout(ctx context.Context, sessionID, ownerID string, now time.Time) error {
	_ = now
	_, err := s.queries().RevokeSession(ctx, gatewayauthdb.RevokeSessionParams{
		ID: sessionID, OwnerUserID: ownerID,
	})
	return s.wrap(err)
}

// errAuthentication collapses lookup misses inside completion transactions
// into the sanitized authentication failure.
func errAuthentication(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrAuthenticationFailed
	}
	return err
}

func errDeviceNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrDeviceNotFound
	}
	return err
}

func marshalSnapshot(snapshot domain.RevocationSnapshot) (json.RawMessage, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: revocation snapshot encode: %w", domain.ErrAuthCorrupt, err)
	}
	return encoded, nil
}

func corruptStored(kind string) error {
	return fmt.Errorf("%w: stored %s invariant", domain.ErrAuthCorrupt, kind)
}

func ticketFromRow(row gatewayauthdb.WorkosGatewayPairingTicket) (domain.PairingTicket, error) {
	ticket := domain.PairingTicket{
		ID: row.ID, OwnerID: row.OwnerUserID, SecretHash: row.SecretHash,
		PublicOrigin: row.PublicOrigin, TLSFingerprint: row.TlsFingerprint,
		State:         domain.TicketState(row.State),
		DeviceID:      uuidString(row.DeviceID),
		PublicKeyHash: textString(row.PublicKeyHash),
		ClaimedName:   textString(row.ClaimedName),
		ClaimedClass:  textString(row.ClaimedClass),
		Attempts:      int(row.Attempts),
		ExpiresAt:     row.ExpiresAt,
		CreatedAt:     row.CreatedAt,
		ClaimedAt:     row.ClaimedAt,
		CompletedAt:   row.CompletedAt,
		RevokedAt:     row.RevokedAt,
	}
	if !domain.ValidUUIDv7(ticket.ID) || !domain.ValidUUIDv7(ticket.OwnerID) ||
		!domain.ValidDigest(ticket.SecretHash) || !validStoredOrigin(ticket.PublicOrigin) ||
		!domain.ValidDigest(ticket.TLSFingerprint) || ticket.Attempts < 0 || ticket.Attempts > 10 ||
		ticket.CreatedAt.IsZero() || !ticket.ExpiresAt.After(ticket.CreatedAt) {
		return domain.PairingTicket{}, corruptStored("pairing ticket")
	}
	claimFields := ticket.DeviceID != "" || ticket.PublicKeyHash != "" || ticket.ClaimedName != "" ||
		ticket.ClaimedClass != "" || ticket.ClaimedAt != nil
	allClaimFields := domain.ValidUUIDv7(ticket.DeviceID) && domain.ValidDigest(ticket.PublicKeyHash) &&
		validStoredName(ticket.ClaimedName) && validStoredClass(ticket.ClaimedClass) &&
		ticket.ClaimedAt != nil && !ticket.ClaimedAt.Before(ticket.CreatedAt) && ticket.ClaimedAt.Before(ticket.ExpiresAt)
	validState := false
	switch ticket.State {
	case domain.TicketPending:
		validState = !claimFields && ticket.CompletedAt == nil && ticket.RevokedAt == nil
	case domain.TicketClaimed:
		validState = allClaimFields && ticket.CompletedAt == nil && ticket.RevokedAt == nil
	case domain.TicketCompleted:
		validState = allClaimFields && ticket.CompletedAt != nil && ticket.RevokedAt == nil &&
			!ticket.CompletedAt.Before(*ticket.ClaimedAt) && ticket.CompletedAt.Before(ticket.ExpiresAt)
	case domain.TicketRevoked:
		validState = ticket.CompletedAt == nil && ticket.RevokedAt != nil &&
			!ticket.RevokedAt.Before(ticket.CreatedAt) && (!claimFields || allClaimFields)
	}
	if !validState {
		return domain.PairingTicket{}, corruptStored("pairing ticket state")
	}
	return ticket, nil
}

func challengeFromRow(row gatewayauthdb.WorkosGatewayDeviceAuthChallenge) (domain.Challenge, error) {
	challenge := domain.Challenge{
		ID: row.ID, Purpose: domain.ChallengePurpose(row.Purpose),
		DeviceID: uuidString(row.DeviceID), TicketID: uuidString(row.TicketID),
		PublicKeyHash: row.PublicKeyHash, Nonce: row.Nonce,
		Attempts: int(row.Attempts), ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt,
		ConsumedAt: row.ConsumedAt, ConsumedByDev: uuidString(row.ConsumedByDevice),
		Result: domain.ChallengeResult(textString(row.Result)),
	}
	if !domain.ValidUUIDv7(challenge.ID) || !domain.ValidDigest(challenge.PublicKeyHash) ||
		len(challenge.Nonce) != domain.SecretBytes || challenge.Attempts < 0 || challenge.Attempts > 10 ||
		challenge.CreatedAt.IsZero() || !challenge.ExpiresAt.After(challenge.CreatedAt) {
		return domain.Challenge{}, corruptStored("challenge")
	}
	switch challenge.Purpose {
	case domain.ChallengePairing:
		if !domain.ValidUUIDv7(challenge.DeviceID) || !domain.ValidUUIDv7(challenge.TicketID) {
			return domain.Challenge{}, corruptStored("pairing challenge binding")
		}
	case domain.ChallengeSession:
		if challenge.TicketID != "" || (challenge.DeviceID != "" && !domain.ValidUUIDv7(challenge.DeviceID)) {
			return domain.Challenge{}, corruptStored("session challenge binding")
		}
	default:
		return domain.Challenge{}, corruptStored("challenge purpose")
	}
	if challenge.ConsumedAt == nil {
		if challenge.Result != "" || challenge.ConsumedByDev != "" {
			return domain.Challenge{}, corruptStored("challenge result")
		}
		return challenge, nil
	}
	if challenge.ConsumedAt.Before(challenge.CreatedAt) || challenge.ConsumedAt.After(challenge.ExpiresAt) {
		return domain.Challenge{}, corruptStored("challenge consumption timestamp")
	}
	switch challenge.Result {
	case domain.ChallengeVerified:
		if !domain.ValidUUIDv7(challenge.ConsumedByDev) || challenge.ConsumedByDev != challenge.DeviceID {
			return domain.Challenge{}, corruptStored("verified challenge result")
		}
	case domain.ChallengeFailed:
		if challenge.ConsumedByDev != "" {
			return domain.Challenge{}, corruptStored("failed challenge result")
		}
	default:
		return domain.Challenge{}, corruptStored("challenge result")
	}
	return challenge, nil
}

func deviceFromRow(row gatewayauthdb.WorkosGatewayDeviceCredential) (domain.Device, error) {
	device := domain.Device{
		ID: row.ID, OwnerID: row.OwnerUserID, Name: row.Name,
		Class:               domain.DeviceClass(row.DeviceClass),
		PublicKeySPKI:       row.PublicKeySpki,
		PublicKeyHash:       row.PublicKeyHash,
		Revision:            row.Revision,
		CreatedAt:           row.CreatedAt,
		LastAuthenticatedAt: row.LastAuthenticatedAt,
		RevokedAt:           row.RevokedAt,
	}
	_, keyHash, keyErr := domain.ParseP256SPKI(device.PublicKeySPKI)
	if !domain.ValidUUIDv7(device.ID) || !domain.ValidUUIDv7(device.OwnerID) ||
		!validStoredName(device.Name) || !validStoredClass(string(device.Class)) || keyErr != nil ||
		!domain.ValidDigest(device.PublicKeyHash) || keyHash != device.PublicKeyHash || device.Revision < 1 ||
		device.CreatedAt.IsZero() || device.LastAuthenticatedAt.Before(device.CreatedAt) ||
		(device.RevokedAt != nil && device.RevokedAt.Before(device.LastAuthenticatedAt)) {
		return domain.Device{}, corruptStored("device credential")
	}
	return device, nil
}

func sessionFromRow(row gatewayauthdb.WorkosGatewayDeviceSession) (domain.DeviceSession, error) {
	session := domain.DeviceSession{
		ID: row.ID, OwnerID: row.OwnerUserID, DeviceID: row.DeviceID,
		TokenHash: row.TokenHash, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		LastSeenAt: row.LastSeenAt, RevokedAt: row.RevokedAt,
	}
	if !domain.ValidUUIDv7(session.ID) || !domain.ValidUUIDv7(session.OwnerID) ||
		!domain.ValidUUIDv7(session.DeviceID) || !domain.ValidDigest(session.TokenHash) ||
		session.CreatedAt.IsZero() || !session.ExpiresAt.After(session.CreatedAt) ||
		(session.LastSeenAt != nil && session.LastSeenAt.Before(session.CreatedAt)) ||
		(session.RevokedAt != nil && session.RevokedAt.Before(session.CreatedAt)) {
		return domain.DeviceSession{}, corruptStored("device session")
	}
	return session, nil
}

func validStoredName(value string) bool {
	name, err := domain.ValidateDeviceName(value)
	return err == nil && name == value
}

func validStoredClass(value string) bool {
	class, err := domain.ParseDeviceClass(value)
	return err == nil && string(class) == value
}

func validStoredOrigin(value string) bool {
	origin, err := url.Parse(value)
	return err == nil && origin.Scheme == "https" && origin.Host != "" && origin.User == nil &&
		origin.Path == "" && origin.RawQuery == "" && origin.Fragment == "" && origin.Opaque == ""
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func uuidOrNil(id string) pgtype.UUID {
	if id == "" {
		return pgtype.UUID{}
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func textOrNil(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
