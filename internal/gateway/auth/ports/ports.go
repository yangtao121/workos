// Package ports declares the dependencies the device authentication
// application service injects: the durable repository, the clock, and the
// process wiring adapters implement them.
package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

// Clock provides the UTC process time; tests inject determinism.
type Clock interface {
	Now() time.Time
}

// Repository is the durable Gateway-owned authority. Every method maps to
// one transactional use case; implementations adjudicate concurrency with
// PostgreSQL locks, guards, and unique indexes — application pre-checks are
// only an optimization, never the arbiter. Failures map onto the domain
// verdict errors.
type Repository interface {
	// Ready pings the auth store for production readiness.
	Ready(ctx context.Context) error

	// RotatePairingTicket atomically revokes every pending ticket of the
	// owner inside the owner-level lock and inserts the new ticket, so
	// concurrent rotations converge on exactly one outstanding ticket.
	RotatePairingTicket(ctx context.Context, ticket domain.PairingTicket) error

	// LoadTicketBySecretHash resolves a still-unexpired ticket by its
	// secret hash; an unknown or expired secret is ErrAuthenticationFailed.
	LoadTicketBySecretHash(ctx context.Context, secretHash string, now time.Time) (domain.PairingTicket, error)

	// LoadTicket resolves one ticket by id and owner.
	LoadTicket(ctx context.Context, id, ownerID string) (domain.PairingTicket, error)

	// ClaimPairingTicket performs the guarded pending→claimed transition,
	// binding the pending device identity (id, key digest, name, class).
	// Losing the race or hitting expiry is ErrAuthenticationFailed.
	ClaimPairingTicket(ctx context.Context, ticketID, ownerID, deviceID, publicKeyHash, deviceName, deviceClass string, now time.Time) (domain.PairingTicket, error)

	// FailTicketAttempt durably counts a failed attempt; the repository
	// stops counting at the bound.
	FailTicketAttempt(ctx context.Context, ticketID string) error

	// CreateChallenge persists one challenge.
	CreateChallenge(ctx context.Context, challenge domain.Challenge) error

	// LoadChallenge resolves one challenge by id.
	LoadChallenge(ctx context.Context, id string) (domain.Challenge, error)

	// ConsumeChallenge performs the single-shot consumption verdict.
	// Zero affected rows (already consumed/expired) is
	// ErrAuthenticationFailed.
	ConsumeChallenge(ctx context.Context, id, deviceID string, result domain.ChallengeResult, now time.Time) error

	// FailChallengeAttempt durably counts a failed attempt.
	FailChallengeAttempt(ctx context.Context, id string) error

	// LoadActiveDevice resolves an active (non-revoked) credential by id,
	// regardless of owner: the unauthenticated session flow learns the
	// owner only from the stored row.
	LoadActiveDevice(ctx context.Context, id string) (domain.Device, error)

	// CompletePairing atomically: locks the claimed ticket, verifies the
	// challenge binding, inserts the device credential (duplicate active
	// key ⇒ ErrAuthenticationFailed), completes the ticket, consumes the
	// challenge, revokes the device's previous sessions, and inserts the
	// new session. op carries the verified proof inputs; the raw token
	// never enters this call — only its hash.
	CompletePairing(ctx context.Context, op CompletePairingOp) (domain.Device, domain.DeviceSession, error)

	// CompleteSession atomically: locks the active device, verifies and
	// consumes the challenge, revokes previous sessions, inserts the new
	// session, and refreshes last_authenticated_at.
	CompleteSession(ctx context.Context, op CompleteSessionOp) (domain.Device, domain.DeviceSession, error)

	// ResolveSession loads one session by token hash with its device for
	// the per-request gate. Unknown hash is ErrAuthenticationFailed.
	ResolveSession(ctx context.Context, tokenHash string) (domain.DeviceSession, domain.Device, error)

	// TouchSessionLastSeen performs the guarded low-frequency last-seen
	// update: no-op unless the stored value is older than threshold.
	TouchSessionLastSeen(ctx context.Context, sessionID string, now, threshold time.Time)

	// ListDevices returns up to limit+1 owner devices newer-scoped by the
	// exclusive id cursor (descending); callers detect pagination with the
	// limit+1 row.
	ListDevices(ctx context.Context, ownerID, cursorUUID string, limit int) ([]domain.Device, error)

	// RevokeDevice atomically: replays a persisted identical request
	// snapshot, or — inside the device row lock — verifies the expected
	// revision, revokes the credential, revokes all its sessions, and
	// persists the idempotency result snapshot. A different request under
	// the same key is ErrConflict; a stale revision is ErrConflict; an
	// owner-foreign or unknown device is ErrDeviceNotFound.
	RevokeDevice(ctx context.Context, op RevokeDeviceOp) (domain.Device, bool, error)

	// Logout revokes exactly the current session.
	Logout(ctx context.Context, sessionID, ownerID string, now time.Time) error
}

// CompletePairingOp carries the fully verified pairing completion facts.
type CompletePairingOp struct {
	TicketID         string
	OwnerID          string
	DeviceID         string
	DeviceName       string
	DeviceClass      string
	PublicKeySPKI    []byte
	PublicKeyHash    string
	ChallengeID      string
	SessionID        string
	SessionTokenHash string
	SessionExpiresAt time.Time
	Now              time.Time
}

// CompleteSessionOp carries the verified session-proof completion facts.
type CompleteSessionOp struct {
	DeviceID         string
	ChallengeID      string
	SessionID        string
	SessionTokenHash string
	SessionExpiresAt time.Time
	Now              time.Time
}

// RevokeDeviceOp carries the owner-scoped idempotent revocation request.
type RevokeDeviceOp struct {
	OwnerID          string
	DeviceID         string
	IdempotencyKey   string
	RequestDigest    string
	ExpectedRevision int64
	Now              time.Time
}
