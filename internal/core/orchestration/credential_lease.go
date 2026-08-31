// Package orchestration — credential lease coordination (ADR-0009). The
// issuer derives every fact from the caller-supplied task lease ID and
// worker ID inside one controlled transaction: the Agent module's
// transaction-scoped authority proves the lease is active and held by that
// worker and returns the task's durable credential snapshot; the Credential
// module's transaction-scoped store proves the exact credential is still
// active at the exact snapshotted revision and adjudicates the durable
// lease row. Plaintext secret material exists only inside the issuer's
// response and only on the first grant of an active lease.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	credentialdomain "github.com/yangtao121/workos/internal/core/credential/domain"
	credentialports "github.com/yangtao121/workos/internal/core/credential/ports"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// TaskCredentialAuthority is the Agent module's transaction-scoped port the
// issuer coordinates with.
type TaskCredentialAuthority interface {
	// ResolveTaskCredential proves the task lease is active and held by
	// workerID, and returns the task's durable credential snapshot facts
	// (Required=false when the provider needs no credential). ErrLeaseLost
	// fails closed.
	ResolveTaskCredential(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (agentports.TaskCredentialFacts, error)
	// TaskLeaseExpiry returns the current expiry of the active task lease
	// held by workerID, or found=false once it is gone.
	TaskLeaseExpiry(ctx context.Context, tx dbtx.Tx, taskLeaseID, workerID string, now time.Time) (time.Time, bool, error)
}

// CredentialLeaseIssuer implements the private CredentialLeaseService
// coordination contract.
type CredentialLeaseIssuer struct {
	pool    *pgxpool.Pool
	tasks   TaskCredentialAuthority
	vault   credentialports.Repository
	cipher  credentialports.Cipher
	ids     ids.Generator
	purpose string
}

func NewCredentialLeaseIssuer(
	pool *pgxpool.Pool, tasks TaskCredentialAuthority, vault credentialports.Repository,
	ciph credentialports.Cipher, generator ids.Generator,
) (*CredentialLeaseIssuer, error) {
	if pool == nil || tasks == nil || vault == nil || generator == nil {
		return nil, errors.New("credential lease issuer requires pool, task authority, vault, and ids")
	}
	// A nil cipher is a vault that is not configured: the issuer stays
	// constructed so the private service exists, but every acquire/renew
	// fails closed instead of serving a lease without crypto.
	return &CredentialLeaseIssuer{
		pool: pool, tasks: tasks, vault: vault, cipher: ciph, ids: generator,
		purpose: credentialdomain.PurposeProviderAPIKeyV1,
	}, nil
}

// Acquire derives the grant. Response-loss replay of the same active task
// lease returns the same lease row and credential revision — never a second
// row — and re-delivers the secret only while the lease stays active and
// worker-owned.
func (i *CredentialLeaseIssuer) Acquire(ctx context.Context, taskLeaseID, workerID string) (credentialports.LeaseGrant, error) {
	if i.cipher == nil {
		return credentialports.LeaseGrant{}, credentialdomain.ErrUnavailable
	}
	now := credentialdomain.CanonicalUTCTime(time.Now())
	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return credentialports.LeaseGrant{}, storeFailure("begin credential acquire", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	facts, err := i.tasks.ResolveTaskCredential(ctx, tx, taskLeaseID, workerID, now)
	if err != nil {
		if errors.Is(err, agentdomain.ErrLeaseLost) {
			return credentialports.LeaseGrant{}, credentialdomain.ErrLeaseLost
		}
		return credentialports.LeaseGrant{}, err
	}
	if !facts.Required {
		// The task snapshot proves this provider needs no credential. This
		// verdict is derived from the lease, never from caller input.
		return credentialports.LeaseGrant{TaskLeaseID: taskLeaseID, Required: false}, nil
	}
	credential, sealed, err := i.vault.SealedCredentialForTask(
		ctx, tx, facts.OwnerUserID, facts.CredentialID, facts.ProviderID, i.purpose, facts.CredentialRevision,
	)
	if err != nil {
		// A rotated or revoked snapshot fails closed with zero side effects.
		return credentialports.LeaseGrant{}, err
	}
	existing, found, err := i.vault.TaskCredentialLease(ctx, tx, taskLeaseID)
	if err != nil {
		return credentialports.LeaseGrant{}, err
	}
	if found {
		if existing.TaskLeaseID != taskLeaseID || existing.TaskID != facts.TaskID ||
			existing.WorkerID != workerID || existing.OwnerUserID != facts.OwnerUserID ||
			existing.ConsumerID != facts.ProviderID || existing.Purpose != i.purpose ||
			existing.CredentialID != facts.CredentialID || existing.CredentialRevision != facts.CredentialRevision ||
			existing.Status != credentialdomain.LeaseStatusActive || !existing.ExpiresAt.After(now) ||
			existing.ExpiresAt.After(facts.TaskLeaseExpiresAt) {
			return credentialports.LeaseGrant{}, credentialdomain.ErrLeaseLost
		}
		secret, err := i.openCredential(sealed, facts, credential)
		if err != nil {
			return credentialports.LeaseGrant{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			overwrite(secret)
			return credentialports.LeaseGrant{}, storeFailure("commit credential acquire replay", err)
		}
		return credentialports.LeaseGrant{
			LeaseID: existing.ID, TaskLeaseID: existing.TaskLeaseID, ConsumerID: existing.ConsumerID,
			Purpose: existing.Purpose, CredentialRevision: existing.CredentialRevision,
			ExpiresAt: existing.ExpiresAt, Secret: secret, Required: true,
		}, nil
	}
	leaseID := i.ids.New()
	secret, err := i.openCredential(sealed, facts, credential)
	if err != nil {
		return credentialports.LeaseGrant{}, err
	}
	_, fresh, err := i.vault.InsertTaskCredentialLease(ctx, tx, credentialports.TaskCredentialLease{
		ID: leaseID, TaskLeaseID: taskLeaseID, TaskID: facts.TaskID, WorkerID: workerID,
		OwnerUserID: facts.OwnerUserID, ConsumerID: facts.ProviderID, Purpose: i.purpose,
		CredentialID: facts.CredentialID, CredentialRevision: facts.CredentialRevision,
		Status: credentialdomain.LeaseStatusActive, ExpiresAt: facts.TaskLeaseExpiresAt, CreatedAt: now,
	})
	if err != nil {
		overwrite(secret)
		return credentialports.LeaseGrant{}, err
	}
	if !fresh {
		// The unique index lost a race but no usable winner row is visible:
		// the physical facts diverged from the protocol. Fail closed.
		overwrite(secret)
		return credentialports.LeaseGrant{}, credentialdomain.ErrLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		overwrite(secret)
		return credentialports.LeaseGrant{}, storeFailure("commit credential acquire", err)
	}
	return credentialports.LeaseGrant{
		LeaseID: leaseID, TaskLeaseID: taskLeaseID, ConsumerID: facts.ProviderID,
		Purpose: i.purpose, CredentialRevision: facts.CredentialRevision,
		ExpiresAt: facts.TaskLeaseExpiresAt, Secret: secret, Required: true,
	}, nil
}

// Renew extends the lease to the current expiry of the underlying active
// task lease and re-proves the credential snapshot is still active at the
// snapshotted revision. A revoked or rotated credential answers valid=false
// so the worker stops its provider child on the next bounded heartbeat.
func (i *CredentialLeaseIssuer) Renew(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string) (credentialports.LeaseVerdict, error) {
	if i.cipher == nil {
		return credentialports.LeaseVerdict{}, credentialdomain.ErrUnavailable
	}
	now := credentialdomain.CanonicalUTCTime(time.Now())
	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return credentialports.LeaseVerdict{}, storeFailure("begin credential renew", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	lease, err := i.vault.ActiveTaskCredentialLease(ctx, tx, credentialLeaseID, taskLeaseID, workerID, now)
	if err != nil {
		if errors.Is(err, credentialdomain.ErrLeaseLost) {
			return credentialports.LeaseVerdict{Valid: false}, nil
		}
		return credentialports.LeaseVerdict{}, err
	}
	expiry, ok, err := i.tasks.TaskLeaseExpiry(ctx, tx, taskLeaseID, workerID, now)
	if err != nil {
		if errors.Is(err, agentdomain.ErrLeaseLost) {
			return credentialports.LeaseVerdict{Valid: false}, nil
		}
		return credentialports.LeaseVerdict{}, err
	}
	if !ok {
		return credentialports.LeaseVerdict{Valid: false}, nil
	}
	// Re-prove the exact credential revision is still active. A revoke or
	// rotate invalidates the lease without ever returning new material.
	if _, _, err := i.vault.SealedCredentialForTask(
		ctx, tx, lease.OwnerUserID, lease.CredentialID, lease.ConsumerID, i.purpose, lease.CredentialRevision,
	); err != nil {
		if errors.Is(err, credentialdomain.ErrNotFound) || errors.Is(err, credentialdomain.ErrLeaseLost) {
			return credentialports.LeaseVerdict{Valid: false}, nil
		}
		return credentialports.LeaseVerdict{}, err
	}
	if err := i.vault.ExtendTaskCredentialLease(ctx, tx, lease.ID, expiry); err != nil {
		return credentialports.LeaseVerdict{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return credentialports.LeaseVerdict{}, storeFailure("commit credential renew", err)
	}
	return credentialports.LeaseVerdict{Valid: true, ExpiresAt: expiry}, nil
}

// Release is idempotent: an already released or expired lease releases
// successfully; unknown or foreign identifiers are lease-lost verdicts.
func (i *CredentialLeaseIssuer) Release(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string) error {
	if err := i.vault.ReleaseTaskCredentialLease(ctx, credentialLeaseID, taskLeaseID, workerID, time.Now().UTC()); err != nil {
		if errors.Is(err, credentialdomain.ErrLeaseLost) {
			return credentialdomain.ErrLeaseLost
		}
		return err
	}
	return nil
}

func storeFailure(stage string, err error) error {
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", stage, credentialdomain.ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

func (i *CredentialLeaseIssuer) openCredential(sealed credentialdomain.SealedMaterial, facts agentports.TaskCredentialFacts, credential credentialdomain.Credential) ([]byte, error) {
	return i.cipher.Open(sealed, credentialports.SealAAD{
		OwnerUserID: facts.OwnerUserID, CredentialID: credential.ID,
		ConsumerID: credential.ConsumerID, Purpose: i.purpose, Revision: credential.Revision,
	})
}

func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}
