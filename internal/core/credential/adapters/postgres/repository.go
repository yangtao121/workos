// Package postgres is the Credential Vault's only durable adapter. Every
// admin mutation is one transaction that physically arbitrates idempotency
// with the request mapping's primary key before any credential row moves;
// a loser re-reads the mapping inside its own transaction and replays or
// conflicts exactly like the Project create protocol (ADR-0004 pattern).
// Secret material is sealed inside the transaction and never leaves it in
// plaintext; lease rows never contain secret material at all.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/credential/adapters/postgres/credentialdb"
	"github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/core/credential/ports"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

const resultVersion = 1

type Repository struct {
	pool    *pgxpool.Pool
	queries *credentialdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: credentialdb.New(pool)}
}

// Put implements ports.Repository.
func (r *Repository) Put(ctx context.Context, ciph ports.Cipher, command ports.PutCommand) (domain.Credential, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Credential{}, storeError("begin credential put", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	queries := r.queries.WithTx(tx)
	credentialID := uuid.Must(uuid.NewV7()).String()
	sealed, err := ciph.Seal(append([]byte(nil), command.Secret...), ports.SealAAD{
		OwnerUserID: command.OwnerUserID, CredentialID: credentialID,
		ConsumerID: command.ConsumerID, Purpose: command.Purpose, Revision: 1,
	})
	if err != nil {
		return domain.Credential{}, storeError("seal credential", err)
	}
	arbitrated, err := queries.InsertCredentialRequest(ctx, credentialdb.InsertCredentialRequestParams{
		OwnerUserID:    command.OwnerUserID,
		IdempotencyKey: command.IdempotencyKey,
		RequestDigest:  command.RequestDigest,
		Result:         inFlightResult(),
		CreatedAt:      timestamp(command.Now),
	})
	if err != nil {
		return domain.Credential{}, storeError("arbitrate credential request", err)
	}
	if arbitrated == 0 {
		return domain.Credential{}, ports.ErrKeyConsumed
	}
	inserted, err := queries.InsertProviderCredential(ctx, credentialdb.InsertProviderCredentialParams{
		ID:          credentialID,
		OwnerUserID: command.OwnerUserID,
		ConsumerID:  command.ConsumerID,
		Purpose:     command.Purpose,
		Label:       command.Label,
		Revision:    1,
		Status:      domain.StatusActive,
		Nonce:       sealed.Nonce,
		Ciphertext:  sealed.Ciphertext,
		KeyEpoch:    ciph.Epoch(),
		CreatedAt:   timestamp(command.Now),
		UpdatedAt:   timestamp(command.Now),
	})
	if err != nil {
		return domain.Credential{}, storeError("insert provider credential", err)
	}
	if inserted == 0 {
		// The active partial unique index rejected a concurrent Put with a
		// different key for the same (owner, consumer, purpose). Roll back —
		// including the request mapping, because failed writes never consume
		// keys — and answer AlreadyExists.
		return domain.Credential{}, ports.ErrActiveExists
	}
	credential := domain.Credential{
		ID: credentialID, OwnerUserID: command.OwnerUserID, ConsumerID: command.ConsumerID,
		Purpose: command.Purpose, Label: command.Label, Revision: 1, Status: domain.StatusActive,
		CreatedAt: command.Now, UpdatedAt: command.Now,
	}
	if err := queries.InsertCredentialAudit(ctx, auditParams(ports.AuditEntry{
		Action: "put", OwnerUserID: command.OwnerUserID, CredentialID: credentialID,
		ConsumerID: command.ConsumerID, Purpose: command.Purpose, Revision: 1,
		KeyEpoch: ciph.Epoch(), Result: "applied", OccurredAt: command.Now,
	})); err != nil {
		return domain.Credential{}, storeError("audit credential put", err)
	}
	if err := finalizeRequest(ctx, queries, command.OwnerUserID, command.IdempotencyKey, credential); err != nil {
		return domain.Credential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Credential{}, storeError("commit credential put", err)
	}
	return credential, nil
}

// Rotate implements ports.Repository.
func (r *Repository) Rotate(ctx context.Context, ciph ports.Cipher, command ports.RotateCommand) (domain.Credential, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Credential{}, storeError("begin credential rotate", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	queries := r.queries.WithTx(tx)
	arbitrated, err := queries.InsertCredentialRequest(ctx, credentialdb.InsertCredentialRequestParams{
		OwnerUserID:    command.OwnerUserID,
		IdempotencyKey: command.IdempotencyKey,
		RequestDigest:  command.RequestDigest,
		Result:         inFlightResult(),
		CreatedAt:      timestamp(command.Now),
	})
	if err != nil {
		return domain.Credential{}, storeError("arbitrate credential rotate", err)
	}
	if arbitrated == 0 {
		return domain.Credential{}, ports.ErrKeyConsumed
	}
	row, err := queries.LockProviderCredential(ctx, command.CredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, storeError("lock credential for rotate", err)
	}
	if row.OwnerUserID != command.OwnerUserID {
		return domain.Credential{}, domain.ErrNotFound
	}
	if row.Status != domain.StatusActive || row.Revision != command.ExpectedRevision {
		return domain.Credential{}, domain.ErrConflict
	}
	if row.KeyEpoch != ciph.Epoch() {
		// A row sealed under a non-current epoch cannot be re-sealed by this
		// cipher: the vault state and the process key diverged. Fail closed.
		return domain.Credential{}, domain.ErrCorrupt
	}
	if !domain.ValidSecret(row.Purpose, command.Secret) {
		// The kind-specific grammar is decided by the stored purpose, which
		// only becomes visible once the row is locked (ADR-0015).
		return domain.Credential{}, domain.ErrInvalid
	}
	revision := row.Revision + 1
	sealed, err := ciph.Seal(append([]byte(nil), command.Secret...), ports.SealAAD{
		OwnerUserID: row.OwnerUserID, CredentialID: row.ID,
		ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: revision,
	})
	if err != nil {
		return domain.Credential{}, storeError("seal rotated credential", err)
	}
	moved, err := queries.UpdateCredentialMaterial(ctx, credentialdb.UpdateCredentialMaterialParams{
		Label:      command.Label,
		Revision:   revision,
		Nonce:      sealed.Nonce,
		Ciphertext: sealed.Ciphertext,
		KeyEpoch:   ciph.Epoch(),
		UpdatedAt:  timestamp(command.Now),
		ID:         row.ID,
	})
	if err != nil {
		return domain.Credential{}, storeError("update credential material", err)
	}
	if moved == 0 {
		return domain.Credential{}, domain.ErrCorrupt
	}
	credential, err := rowToCredential(row)
	if err != nil {
		return domain.Credential{}, err
	}
	credential.Label, credential.Revision, credential.UpdatedAt = command.Label, revision, command.Now
	if err := queries.InsertCredentialAudit(ctx, auditParams(ports.AuditEntry{
		Action: "rotate", OwnerUserID: command.OwnerUserID, CredentialID: row.ID,
		ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: revision,
		KeyEpoch: ciph.Epoch(), Result: "applied", OccurredAt: command.Now,
	})); err != nil {
		return domain.Credential{}, storeError("audit credential rotate", err)
	}
	if err := finalizeRequest(ctx, queries, command.OwnerUserID, command.IdempotencyKey, credential); err != nil {
		return domain.Credential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Credential{}, storeError("commit credential rotate", err)
	}
	return credential, nil
}

// Revoke implements ports.Repository.
func (r *Repository) Revoke(ctx context.Context, command ports.RevokeCommand) (domain.Credential, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Credential{}, storeError("begin credential revoke", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	queries := r.queries.WithTx(tx)
	arbitrated, err := queries.InsertCredentialRequest(ctx, credentialdb.InsertCredentialRequestParams{
		OwnerUserID:    command.OwnerUserID,
		IdempotencyKey: command.IdempotencyKey,
		RequestDigest:  command.RequestDigest,
		Result:         inFlightResult(),
		CreatedAt:      timestamp(command.Now),
	})
	if err != nil {
		return domain.Credential{}, storeError("arbitrate credential revoke", err)
	}
	if arbitrated == 0 {
		return domain.Credential{}, ports.ErrKeyConsumed
	}
	row, err := queries.LockProviderCredential(ctx, command.CredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, storeError("lock credential for revoke", err)
	}
	if row.OwnerUserID != command.OwnerUserID {
		return domain.Credential{}, domain.ErrNotFound
	}
	if row.Status == domain.StatusRevoked {
		return domain.Credential{}, domain.ErrConflict
	}
	if row.Revision != command.ExpectedRevision {
		return domain.Credential{}, domain.ErrConflict
	}
	revision := row.Revision + 1
	moved, err := queries.RevokeProviderCredential(ctx, credentialdb.RevokeProviderCredentialParams{
		Revision: revision, UpdatedAt: timestamp(command.Now), ID: row.ID,
	})
	if err != nil {
		return domain.Credential{}, storeError("revoke provider credential", err)
	}
	if moved == 0 {
		return domain.Credential{}, domain.ErrCorrupt
	}
	credential, err := rowToCredential(row)
	if err != nil {
		return domain.Credential{}, err
	}
	credential.Status, credential.Revision, credential.UpdatedAt = domain.StatusRevoked, revision, command.Now
	if err := queries.InsertCredentialAudit(ctx, auditParams(ports.AuditEntry{
		Action: "revoke", OwnerUserID: command.OwnerUserID, CredentialID: row.ID,
		ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: revision,
		KeyEpoch: row.KeyEpoch, Result: "applied", OccurredAt: command.Now,
	})); err != nil {
		return domain.Credential{}, storeError("audit credential revoke", err)
	}
	if err := finalizeRequest(ctx, queries, command.OwnerUserID, command.IdempotencyKey, credential); err != nil {
		return domain.Credential{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Credential{}, storeError("commit credential revoke", err)
	}
	return credential, nil
}

// List implements ports.Repository.
func (r *Repository) List(ctx context.Context, ownerUserID string) ([]domain.Credential, error) {
	rows, err := r.queries.ListOwnerCredentials(ctx, ownerUserID)
	if err != nil {
		return nil, storeError("list credentials", err)
	}
	result := make([]domain.Credential, 0, len(rows))
	for _, row := range rows {
		credential, err := rowToCredential(row)
		if err != nil {
			return nil, err
		}
		result = append(result, credential)
	}
	return result, nil
}

// GetCredentialRequest implements ports.Repository.
func (r *Repository) GetCredentialRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.RequestRecord, bool, error) {
	row, err := r.queries.GetCredentialRequest(ctx, credentialdb.GetCredentialRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.RequestRecord{}, false, nil
	}
	if err != nil {
		return ports.RequestRecord{}, false, storeError("read credential request", err)
	}
	record := ports.RequestRecord{RequestDigest: row.RequestDigest, Result: row.Result}
	if isJSONNull(record.Result) {
		// A mapping stuck in the in-flight marker proves the winning
		// transaction crashed before commit; the mapping itself rolled back
		// with it, so a visible in-flight row is stored corruption.
		return ports.RequestRecord{}, false, domain.ErrCorrupt
	}
	return record, true, nil
}

// ActiveCredential implements ports.Repository.
func (r *Repository) ActiveCredential(ctx context.Context, ownerUserID, consumerID, purpose string) (domain.Credential, error) {
	row, err := r.queries.GetActiveCredential(ctx, credentialdb.GetActiveCredentialParams{
		OwnerUserID: ownerUserID, ConsumerID: consumerID, Purpose: purpose,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, storeError("read active credential", err)
	}
	credential, err := rowToCredential(row)
	if err != nil {
		return domain.Credential{}, err
	}
	return credential, nil
}

// CredentialByID implements ports.Repository.
func (r *Repository) CredentialByID(ctx context.Context, ownerUserID, credentialID string) (domain.Credential, error) {
	row, err := r.queries.GetCredentialByID(ctx, credentialdb.GetCredentialByIDParams{
		ID: credentialID, OwnerUserID: ownerUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, storeError("read credential", err)
	}
	credential, err := rowToCredential(row)
	if err != nil {
		return domain.Credential{}, err
	}
	return credential, nil
}

// SealedCredentialForTask implements ports.Repository: the exact credential
// identity must still be active at the exact snapshot revision, or the lease
// derivation fails closed.
func (r *Repository) SealedCredentialForTask(ctx context.Context, tx dbtx.Tx, ownerUserID, credentialID, consumerID, purpose string, revision, keyEpoch int64) (domain.Credential, domain.SealedMaterial, error) {
	row, err := r.queries.WithTx(tx).LockSealedCredentialForTask(ctx, credentialdb.LockSealedCredentialForTaskParams{
		ID: credentialID, OwnerUserID: ownerUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, domain.SealedMaterial{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, domain.SealedMaterial{}, storeError("lock credential for lease", err)
	}
	if row.OwnerUserID != ownerUserID || row.ConsumerID != consumerID || row.Purpose != purpose ||
		row.Status != domain.StatusActive || row.Revision != revision {
		return domain.Credential{}, domain.SealedMaterial{}, domain.ErrLeaseLost
	}
	if row.KeyEpoch != keyEpoch {
		return domain.Credential{}, domain.SealedMaterial{}, domain.ErrCorrupt
	}
	credential, err := rowToCredential(row)
	if err != nil {
		return domain.Credential{}, domain.SealedMaterial{}, err
	}
	return credential, domain.SealedMaterial{Nonce: row.Nonce, Ciphertext: row.Ciphertext}, nil
}

// TaskCredentialLease implements ports.Repository.
func (r *Repository) TaskCredentialLease(ctx context.Context, tx dbtx.Tx, taskLeaseID string) (ports.TaskCredentialLease, bool, error) {
	row, err := r.queries.WithTx(tx).GetTaskCredentialLease(ctx, taskLeaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.TaskCredentialLease{}, false, nil
	}
	if err != nil {
		return ports.TaskCredentialLease{}, false, storeError("read task credential lease", err)
	}
	lease, err := leaseToPorts(row)
	if err != nil {
		return ports.TaskCredentialLease{}, false, err
	}
	return lease, true, nil
}

// InsertTaskCredentialLease implements ports.Repository.
func (r *Repository) InsertTaskCredentialLease(ctx context.Context, tx dbtx.Tx, lease ports.TaskCredentialLease) (ports.TaskCredentialLease, bool, error) {
	if !validTaskCredentialLease(lease, true) {
		return ports.TaskCredentialLease{}, false, domain.ErrCorrupt
	}
	inserted, err := r.queries.WithTx(tx).InsertTaskCredentialLease(ctx, credentialdb.InsertTaskCredentialLeaseParams{
		ID: lease.ID, TaskLeaseID: lease.TaskLeaseID, TaskID: lease.TaskID, WorkerID: lease.WorkerID,
		OwnerUserID: lease.OwnerUserID, ConsumerID: lease.ConsumerID, Purpose: lease.Purpose,
		CredentialID: lease.CredentialID, CredentialRevision: lease.CredentialRevision,
		ExpiresAt: timestamp(lease.ExpiresAt), CreatedAt: timestamp(lease.CreatedAt),
	})
	if err != nil {
		return ports.TaskCredentialLease{}, false, storeError("insert task credential lease", err)
	}
	if inserted == 1 {
		return lease, true, nil
	}
	existing, found, err := r.TaskCredentialLease(ctx, tx, lease.TaskLeaseID)
	if err != nil || !found {
		// The unique index rejected us but the row is unreadable inside this
		// transaction: the physical facts diverged from the protocol.
		return ports.TaskCredentialLease{}, false, domain.ErrCorrupt
	}
	return existing, false, nil
}

// RenewTaskCredentialLease implements ports.Repository.
func (r *Repository) RenewTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID, taskLeaseID, workerID string, expiresAt, now time.Time) (ports.TaskCredentialLease, error) {
	row, err := r.queries.WithTx(tx).RenewTaskCredentialLease(ctx, credentialdb.RenewTaskCredentialLeaseParams{
		ExpiresAt: expiresAt, ID: credentialLeaseID, TaskLeaseID: taskLeaseID,
		WorkerID: workerID, ExpiresAt_2: now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.TaskCredentialLease{}, domain.ErrLeaseLost
	}
	if err != nil {
		return ports.TaskCredentialLease{}, storeError("renew task credential lease", err)
	}
	return leaseToPorts(row)
}

// ActiveTaskCredentialLease implements ports.Repository.
func (r *Repository) ActiveTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID, taskLeaseID, workerID string, now time.Time) (ports.TaskCredentialLease, error) {
	row, err := r.queries.WithTx(tx).LockActiveTaskCredentialLease(ctx, credentialdb.LockActiveTaskCredentialLeaseParams{
		ID: credentialLeaseID, TaskLeaseID: taskLeaseID, WorkerID: workerID, ExpiresAt: timestamp(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.TaskCredentialLease{}, domain.ErrLeaseLost
	}
	if err != nil {
		return ports.TaskCredentialLease{}, storeError("lock task credential lease", err)
	}
	return leaseToPorts(row)
}

// ExtendTaskCredentialLease implements ports.Repository.
func (r *Repository) ExtendTaskCredentialLease(ctx context.Context, tx dbtx.Tx, credentialLeaseID string, expiresAt time.Time) error {
	moved, err := r.queries.WithTx(tx).ExtendTaskCredentialLease(ctx, credentialdb.ExtendTaskCredentialLeaseParams{
		ExpiresAt: expiresAt, ID: credentialLeaseID,
	})
	if err != nil {
		return storeError("extend task credential lease", err)
	}
	if moved == 0 {
		return domain.ErrCorrupt
	}
	return nil
}

// ReleaseTaskCredentialLease implements ports.Repository. Releasing an
// already released or expired lease is idempotent success.
func (r *Repository) ReleaseTaskCredentialLease(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string, now time.Time) error {
	releasedAt := now
	released, err := r.queries.ReleaseTaskCredentialLease(ctx, credentialdb.ReleaseTaskCredentialLeaseParams{
		ReleasedAt: &releasedAt, ID: credentialLeaseID, TaskLeaseID: taskLeaseID, WorkerID: workerID,
	})
	if err != nil {
		return storeError("release task credential lease", err)
	}
	if released == 0 {
		// Either already released/expired (idempotent success) or unknown/
		// foreign identifiers. Distinguish without leaking: unknown rows are
		// ErrLeaseLost.
		row, readErr := r.queries.GetTaskCredentialLeaseByLeaseID(ctx, credentialLeaseID)
		if errors.Is(readErr, pgx.ErrNoRows) {
			return domain.ErrLeaseLost
		}
		if readErr != nil {
			return storeError("read released task credential lease", readErr)
		}
		lease, convertErr := leaseToPorts(row)
		if convertErr != nil {
			return convertErr
		}
		if lease.TaskLeaseID != taskLeaseID || lease.WorkerID != workerID {
			return domain.ErrLeaseLost
		}
	}
	return nil
}

// ExpireStaleTaskCredentialLeases implements ports.Repository.
func (r *Repository) ExpireStaleTaskCredentialLeases(ctx context.Context, now time.Time) (int64, error) {
	expired, err := r.queries.ExpireStaleTaskCredentialLeases(ctx, timestamp(now))
	if err != nil {
		return 0, storeError("expire task credential leases", err)
	}
	return expired, nil
}

// VaultEpoch implements ports.Repository: the singleton authoritative
// master-key epoch. A missing state row is stored corruption, never a
// silent fallback to epoch 1.
func (r *Repository) VaultEpoch(ctx context.Context) (int64, error) {
	state, err := r.queries.GetVaultState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrCorrupt
	}
	if err != nil {
		return 0, storeError("read credential vault state", err)
	}
	if !state.Singleton || state.CurrentEpoch < 1 {
		return 0, domain.ErrCorrupt
	}
	return state.CurrentEpoch, nil
}

// Reveal implements ports.Repository: one bounded decryption of an active
// credential with the reveal audit row committed in the same transaction
// before the secret leaves the adapter (ADR-0015).
func (r *Repository) Reveal(ctx context.Context, ciph ports.Cipher, command ports.RevealCommand) (domain.Credential, []byte, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Credential{}, nil, storeError("begin credential reveal", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	queries := r.queries.WithTx(tx)
	row, err := queries.LockOwnerCredential(ctx, credentialdb.LockOwnerCredentialParams{
		ID: command.CredentialID, OwnerUserID: command.OwnerUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Credential{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return domain.Credential{}, nil, storeError("lock credential for reveal", err)
	}
	if row.Status != domain.StatusActive {
		return domain.Credential{}, nil, domain.ErrRevoked
	}
	if command.ExpectedRevision != 0 && row.Revision != command.ExpectedRevision {
		return domain.Credential{}, nil, domain.ErrConflict
	}
	if row.KeyEpoch != ciph.Epoch() {
		return domain.Credential{}, nil, domain.ErrCorrupt
	}
	secret, err := ciph.Open(domain.SealedMaterial{Nonce: row.Nonce, Ciphertext: row.Ciphertext}, ports.SealAAD{
		OwnerUserID: row.OwnerUserID, CredentialID: row.ID,
		ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: row.Revision,
	})
	if err != nil {
		return domain.Credential{}, nil, err
	}
	credential, err := rowToCredential(row)
	if err != nil {
		overwrite(secret)
		return domain.Credential{}, nil, err
	}
	if err := queries.InsertCredentialAudit(ctx, auditParams(ports.AuditEntry{
		Action: "reveal", OwnerUserID: command.OwnerUserID, CredentialID: row.ID,
		ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: row.Revision,
		KeyEpoch: row.KeyEpoch, Result: "revealed", OccurredAt: command.Now,
	})); err != nil {
		overwrite(secret)
		return domain.Credential{}, nil, storeError("audit credential reveal", err)
	}
	if err := tx.Commit(ctx); err != nil {
		overwrite(secret)
		return domain.Credential{}, nil, storeError("commit credential reveal", err)
	}
	return credential, secret, nil
}

// RotateMasterKey implements ports.Repository: one transaction re-seals
// every credential under the successor epoch and advances the singleton
// state. Any open/update failure aborts the whole transaction — the vault is
// never half-encrypted — and the caller retries against the unchanged old
// epoch (ADR-0015).
func (r *Repository) RotateMasterKey(ctx context.Context, current, next ports.Cipher, command ports.RotateMasterKeyCommand) (ports.RotateMasterKeyResult, error) {
	result := ports.RotateMasterKeyResult{FromEpoch: current.Epoch(), ToEpoch: next.Epoch()}
	if current.Epoch() == next.Epoch() {
		return result, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return result, storeError("begin master-key rotation", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	queries := r.queries.WithTx(tx)
	state, err := queries.GetVaultState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, domain.ErrCorrupt
	}
	if err != nil {
		return result, storeError("read vault state for rotation", err)
	}
	if state.CurrentEpoch != current.Epoch() {
		// Another rotation won the race. Nothing was written; the caller
		// reloads against the moved epoch and re-adjudicates.
		return result, domain.ErrConflict
	}
	rows, err := queries.LockAllProviderCredentials(ctx)
	if err != nil {
		return result, storeError("lock credentials for rotation", err)
	}
	for _, row := range rows {
		// Revoked rows are frozen at their sealing epoch (ADR-0015): no live
		// path ever opens them again, so rotation leaves them untouched
		// instead of letting one lost legacy key block every future rotation.
		if row.Status != domain.StatusActive {
			continue
		}
		if row.KeyEpoch != current.Epoch() {
			return result, domain.ErrCorrupt
		}
		aad := ports.SealAAD{
			OwnerUserID: row.OwnerUserID, CredentialID: row.ID,
			ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: row.Revision,
		}
		plaintext, err := current.Open(domain.SealedMaterial{Nonce: row.Nonce, Ciphertext: row.Ciphertext}, aad)
		if err != nil {
			// Authentication failure: the whole rotation aborts so no
			// credential is left under a mixed epoch state.
			return result, err
		}
		sealed, err := next.Seal(plaintext, aad)
		if err != nil {
			return result, storeError("re-seal credential for rotation", err)
		}
		moved, err := queries.UpdateCredentialSeal(ctx, credentialdb.UpdateCredentialSealParams{
			Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext,
			KeyEpoch: next.Epoch(), UpdatedAt: timestamp(command.Now), ID: row.ID,
		})
		if err != nil {
			return result, storeError("update credential seal for rotation", err)
		}
		if moved == 0 {
			return result, domain.ErrCorrupt
		}
		result.RotatedCount++
	}
	advanced, err := queries.RotateVaultState(ctx, credentialdb.RotateVaultStateParams{
		CurrentEpoch: next.Epoch(), UpdatedAt: timestamp(command.Now), CurrentEpoch_2: current.Epoch(),
	})
	if err != nil {
		return result, storeError("advance vault epoch", err)
	}
	if advanced == 0 {
		return result, domain.ErrConflict
	}
	if err := queries.InsertCredentialAudit(ctx, auditParams(ports.AuditEntry{
		Action: "rotate-master-key", KeyEpoch: next.Epoch(),
		Result: fmt.Sprintf("applied %d", result.RotatedCount), OccurredAt: command.Now,
	})); err != nil {
		return result, storeError("audit master-key rotation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, storeError("commit master-key rotation", err)
	}
	return result, nil
}

// RecordAudit implements ports.Repository: one audit fact in its own
// transaction for refused vault-wide attempts. A failure fails the caller —
// audit is never silently dropped.
func (r *Repository) RecordAudit(ctx context.Context, entry ports.AuditEntry) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError("begin audit record", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	if err := r.queries.WithTx(tx).InsertCredentialAudit(ctx, auditParams(entry)); err != nil {
		return storeError("record credential audit", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storeError("commit audit record", err)
	}
	return nil
}

// VerifyCurrentKey implements ports.Repository: it proves the candidate
// cipher opens every stored credential without writing anything (ADR-0015).
func (r *Repository) VerifyCurrentKey(ctx context.Context, ciph ports.Cipher) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError("begin key verification", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- read-only verification
	rows, err := r.queries.WithTx(tx).LockAllCredentialRows(ctx)
	if err != nil {
		return storeError("lock credentials for verification", err)
	}
	for _, row := range rows {
		if row.Status != domain.StatusActive {
			continue
		}
		if row.KeyEpoch != ciph.Epoch() {
			return domain.ErrCorrupt
		}
		if _, err := ciph.Open(domain.SealedMaterial{Nonce: row.Nonce, Ciphertext: row.Ciphertext}, ports.SealAAD{
			OwnerUserID: row.OwnerUserID, CredentialID: row.ID,
			ConsumerID: row.ConsumerID, Purpose: row.Purpose, Revision: row.Revision,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// auditParams fills the generated audit insert parameters, minting the row
// identity here so callers stay fact-only. Empty optional facts persist as
// SQL NULL; the audit schema never stores secret material.
func auditParams(entry ports.AuditEntry) credentialdb.InsertCredentialAuditParams {
	return credentialdb.InsertCredentialAuditParams{
		ID:           uuid.Must(uuid.NewV7()).String(),
		OccurredAt:   timestamp(entry.OccurredAt),
		Action:       entry.Action,
		OwnerUserID:  nullableUUID(entry.OwnerUserID),
		CredentialID: nullableUUID(entry.CredentialID),
		ConsumerID:   nullableText(entry.ConsumerID),
		Purpose:      nullableText(entry.Purpose),
		Revision:     nullableInt(entry.Revision),
		KeyEpoch:     nullableInt(entry.KeyEpoch),
		Result:       entry.Result,
	}
}

func nullableUUID(value string) pgtype.UUID {
	parsed, err := uuid.Parse(value)
	if value == "" || err != nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: value, Valid: true}
}

func nullableInt(value int64) pgtype.Int8 {
	if value == 0 {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: value, Valid: true}
}

// overwrite is best-effort buffer hygiene for decrypted bytes that never
// leave the adapter on a failure path.
func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}

func finalizeRequest(ctx context.Context, queries *credentialdb.Queries, ownerUserID, idempotencyKey string, credential domain.Credential) error {
	snapshot, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode credential result snapshot: %w", err)
	}
	if err := queries.FinalizeCredentialRequest(ctx, credentialdb.FinalizeCredentialRequestParams{
		Result: snapshot, OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	}); err != nil {
		return storeError("finalize credential request", err)
	}
	return nil
}

func inFlightResult() []byte { return []byte("null") }

func isJSONNull(value []byte) bool {
	return len(value) == 0 || string(value) == "null"
}

func rowToCredential(value any) (domain.Credential, error) {
	credentialFrom := func(id, ownerUserID, consumerID, purpose, label string, revision int64, status string, createdAt, updatedAt time.Time) (domain.Credential, error) {
		credential := domain.Credential{
			ID: id, OwnerUserID: ownerUserID, ConsumerID: consumerID,
			Purpose: purpose, Label: label, Revision: revision, Status: status,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		}
		if !domain.ValidCredential(credential) {
			return domain.Credential{}, domain.ErrCorrupt
		}
		return credential, nil
	}
	switch value := value.(type) {
	case credentialdb.LockProviderCredentialRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.LockSealedCredentialForTaskRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.LockAllProviderCredentialsRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.LockOwnerCredentialRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.GetActiveCredentialRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.GetCredentialByIDRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	case credentialdb.ListOwnerCredentialsRow:
		return credentialFrom(value.ID, value.OwnerUserID, value.ConsumerID, value.Purpose,
			value.Label, value.Revision, value.Status, value.CreatedAt, value.UpdatedAt)
	default:
		return domain.Credential{}, domain.ErrCorrupt
	}
}

func leaseToPorts(row any) (ports.TaskCredentialLease, error) {
	switch value := row.(type) {
	case credentialdb.GetTaskCredentialLeaseRow:
		return taskCredentialLease(value.ID, value.TaskLeaseID, value.TaskID, value.WorkerID, value.OwnerUserID,
			value.ConsumerID, value.Purpose, value.CredentialID, value.CredentialRevision, value.Status, value.ExpiresAt)
	case credentialdb.GetTaskCredentialLeaseByLeaseIDRow:
		return taskCredentialLease(value.ID, value.TaskLeaseID, value.TaskID, value.WorkerID, value.OwnerUserID,
			value.ConsumerID, value.Purpose, value.CredentialID, value.CredentialRevision, value.Status, value.ExpiresAt)
	case credentialdb.LockActiveTaskCredentialLeaseRow:
		return taskCredentialLease(value.ID, value.TaskLeaseID, value.TaskID, value.WorkerID, value.OwnerUserID,
			value.ConsumerID, value.Purpose, value.CredentialID, value.CredentialRevision, value.Status, value.ExpiresAt)
	case credentialdb.RenewTaskCredentialLeaseRow:
		return taskCredentialLease(value.ID, value.TaskLeaseID, value.TaskID, value.WorkerID, value.OwnerUserID,
			value.ConsumerID, value.Purpose, value.CredentialID, value.CredentialRevision, value.Status, value.ExpiresAt)
	default:
		return ports.TaskCredentialLease{}, domain.ErrCorrupt
	}
}

func taskCredentialLease(id, taskLeaseID, taskID, workerID, ownerUserID, consumerID, purpose, credentialID string,
	credentialRevision int64, status string, expiresAt time.Time,
) (ports.TaskCredentialLease, error) {
	lease := ports.TaskCredentialLease{
		ID: id, TaskLeaseID: taskLeaseID, TaskID: taskID, WorkerID: workerID,
		OwnerUserID: ownerUserID, ConsumerID: consumerID, Purpose: purpose,
		CredentialID: credentialID, CredentialRevision: credentialRevision,
		Status: status, ExpiresAt: expiresAt,
	}
	if !validTaskCredentialLease(lease, false) {
		return ports.TaskCredentialLease{}, domain.ErrCorrupt
	}
	return lease, nil
}

func validTaskCredentialLease(lease ports.TaskCredentialLease, requireCreatedAt bool) bool {
	if !domain.ValidCredentialID(lease.ID) || !domain.ValidCredentialID(lease.TaskLeaseID) ||
		!domain.ValidCredentialID(lease.TaskID) || !domain.ValidWorkerID(lease.WorkerID) ||
		!domain.ValidCredentialID(lease.OwnerUserID) || !domain.ValidConsumerID(lease.ConsumerID) ||
		!domain.ValidPurpose(lease.Purpose) || !domain.ValidCredentialID(lease.CredentialID) ||
		!domain.ValidRevision(lease.CredentialRevision) || !domain.ValidStoredUTCTime(lease.ExpiresAt) {
		return false
	}
	if lease.Status != domain.LeaseStatusActive && lease.Status != domain.LeaseStatusReleased &&
		lease.Status != domain.LeaseStatusExpired {
		return false
	}
	if !requireCreatedAt {
		return true
	}
	return domain.ValidStoredUTCTime(lease.CreatedAt) && lease.ExpiresAt.After(lease.CreatedAt)
}

func timestamp(value time.Time) time.Time { return value }

// storeError classifies driver failures at the port boundary: transient
// classes wrap ErrStoreUnavailable for transports to map to sanitized
// Unavailable; everything else stays an opaque wrapped error (transport maps
// unknown to Internal without echoing driver detail).
func storeError(stage string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", stage, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}
