package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// SealAAD is the associated-data fact set every AEAD operation binds. A
// ciphertext only opens for exactly the credential identity and revision it
// was sealed under, so a row swapped between credentials or revisions fails
// authentication instead of decrypting.
type SealAAD struct {
	OwnerUserID  string
	CredentialID string
	ConsumerID   string
	Purpose      string
	Revision     int64
}

// Cipher is the authenticated-encryption boundary of the vault. The master
// key never leaves the adapter; the application layer only sees sealed
// material and derived (keyed) digests.
type Cipher interface {
	// Seal encrypts plaintext with a fresh CSPRNG nonce under the versioned
	// format. Implementations make a best-effort attempt to overwrite
	// caller-controlled buffers, but Go provides no formal zeroization
	// guarantee for runtime, exec, or string copies.
	Seal(plaintext []byte, aad SealAAD) (domain.SealedMaterial, error)
	// Open authenticates and decrypts. Any authentication failure is
	// domain.ErrCorrupt — stored corruption — never a fallback to plaintext.
	Open(material domain.SealedMaterial, aad SealAAD) ([]byte, error)
	// RequestDigest derives the versioned keyed idempotency digest of one
	// canonical admin request. A leaked database therefore cannot verify
	// guesses of the secret offline.
	RequestDigest(canonical []byte) string
	// VerifyDigest reports in constant time whether canonical produces
	// exactly the stored digest.
	VerifyDigest(canonical []byte, stored string) bool
}

// PutCommand is one create request; Secret is plaintext material that never
// persists beyond the Seal call inside the repository transaction.
// RequestDigest is the application-computed keyed digest of the canonical
// request (including the secret bytes); the repository persists it verbatim.
type PutCommand struct {
	OwnerUserID    string
	ConsumerID     string
	Purpose        string
	Label          string
	Secret         []byte
	IdempotencyKey string
	RequestDigest  string
	Now            time.Time
}

// RotateCommand replaces the secret material of one logical credential; the
// ID survives, revision increases by exactly one.
type RotateCommand struct {
	OwnerUserID      string
	CredentialID     string
	Label            string
	Secret           []byte
	ExpectedRevision int64
	IdempotencyKey   string
	RequestDigest    string
	Now              time.Time
}

// RevokeCommand irreversibly revokes one credential; revision increases by
// exactly one and the status can never return to active.
type RevokeCommand struct {
	OwnerUserID      string
	CredentialID     string
	ExpectedRevision int64
	IdempotencyKey   string
	RequestDigest    string
	Now              time.Time
}

// ErrStoreUnavailable marks a temporarily unreachable vault store. The
// postgres adapter wraps transient driver failures with it at the port
// boundary; transports map it to a sanitized retryable Unavailable.
var ErrStoreUnavailable = errors.New("credential vault store is temporarily unavailable")

// ErrKeyConsumed is returned by mutating repository methods when the
// idempotency mapping already exists. The application re-adjudicates from
// the stored mapping: same canonical digest replays the first response, any
// other digest is a stable conflict.
var ErrKeyConsumed = errors.New("credential idempotency key was already consumed")

// ErrActiveExists marks a Put rejected by the active partial unique index:
// an active credential already exists for the (owner, consumer, purpose).
var ErrActiveExists = errors.New("an active credential already exists for this consumer and purpose")

// RequestRecord is one consumed admin idempotency key with its keyed request
// digest and versioned first-response snapshot.
type RequestRecord struct {
	RequestDigest string
	Result        []byte // jsonb result_version 1 snapshot; nil while in flight
}

// Repository is the Credential-owned storage port. Mutating methods each run
// in one database transaction owned by the adapter and adjudicate their own
// idempotency physical arbitration. Lease methods take the composition
// layer's transaction so the task-lease derivation and the lease insert
// commit atomically.
type Repository interface {
	Put(ctx context.Context, cipher Cipher, command PutCommand) (domain.Credential, error)
	Rotate(ctx context.Context, cipher Cipher, command RotateCommand) (domain.Credential, error)
	Revoke(ctx context.Context, command RevokeCommand) (domain.Credential, error)
	List(ctx context.Context, ownerUserID string) ([]domain.Credential, error)
	// GetCredentialRequest reads one consumed admin key for replay
	// adjudication; found=false means the key is free.
	GetCredentialRequest(ctx context.Context, ownerUserID, idempotencyKey string) (RequestRecord, bool, error)

	// ActiveCredential resolves the owner's active credential for one
	// (consumer, purpose). Unknown or revoked facts surface as
	// domain.ErrNotFound without leaking which.
	ActiveCredential(ctx context.Context, ownerUserID, consumerID, purpose string) (domain.Credential, error)
	// CredentialByID resolves one credential by exact ID and owner.
	CredentialByID(ctx context.Context, ownerUserID, credentialID string) (domain.Credential, error)

	// SealedCredentialForTask reads the sealed material of one credential
	// inside the caller's transaction, proving owner/consumer/purpose/
	// status/revision still match the snapshot before any lease is minted.
	SealedCredentialForTask(ctx context.Context, tx dbtx.Tx, ownerUserID, credentialID, consumerID, purpose string, revision int64) (domain.Credential, domain.SealedMaterial, error)
	// TaskCredentialLease reads the durable lease row for one task lease
	// inside the caller's transaction; found=false means none exists yet.
	TaskCredentialLease(ctx context.Context, tx dbtx.Tx, taskLeaseID string) (TaskCredentialLease, bool, error)
	// InsertTaskCredentialLease persists one lease row; a concurrent same
	// task-lease insert is physically arbitrated by the unique index and
	// returns found=true with the winner's row.
	InsertTaskCredentialLease(ctx context.Context, tx dbtx.Tx, lease TaskCredentialLease) (TaskCredentialLease, bool, error)
	// RenewTaskCredentialLease extends one active lease inside the caller's
	// transaction. A lost, expired, released, or foreign lease is
	// domain.ErrLeaseLost.
	RenewTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID, taskLeaseID, workerID string, expiresAt, now time.Time) (TaskCredentialLease, error)
	// ActiveTaskCredentialLease locks and reads one active, unexpired,
	// worker-owned lease inside the caller's transaction; any other state is
	// domain.ErrLeaseLost.
	ActiveTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID, taskLeaseID, workerID string, now time.Time) (TaskCredentialLease, error)
	// ExtendTaskCredentialLease moves one lease's expiry inside the caller's
	// transaction.
	ExtendTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID string, expiresAt time.Time) error
	// ReleaseTaskCredentialLease marks one active lease released; releasing
	// an already released or expired lease is idempotent success.
	ReleaseTaskCredentialLease(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string, now time.Time) error
	// ExpireStaleTaskCredentialLeases marks active leases past expiry as
	// expired; it returns the number of rows moved.
	ExpireStaleTaskCredentialLeases(ctx context.Context, now time.Time) (int64, error)
}

// TaskCredentialLease is the durable short lease fact. It never carries
// secret material.
type TaskCredentialLease struct {
	ID                 string
	TaskLeaseID        string
	TaskID             string
	WorkerID           string
	OwnerUserID        string
	ConsumerID         string
	Purpose            string
	CredentialID       string
	CredentialRevision int64
	Status             string
	ExpiresAt          time.Time
	CreatedAt          time.Time
}

// Lease grant/verdict projections returned by the composition coordinator.
type LeaseGrant struct {
	LeaseID            string
	TaskLeaseID        string
	ConsumerID         string
	Purpose            string
	CredentialRevision int64
	ExpiresAt          time.Time
	Secret             []byte
	Required           bool
}

type LeaseVerdict struct {
	Valid     bool
	ExpiresAt time.Time
}

// TaskCredentialFacts is what the Agent authority derives from an active
// task lease: whether the task needs a credential at all, and if so the
// exact snapshotted credential identity.
type TaskCredentialFacts struct {
	Required           bool
	TaskID             string
	OwnerUserID        string
	ProviderID         string
	CredentialID       string
	CredentialRevision int64
	TaskLeaseExpiresAt time.Time
}
