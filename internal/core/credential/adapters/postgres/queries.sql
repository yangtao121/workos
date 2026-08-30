-- Credential Vault owned queries (ADR-0009). Everything here touches only
-- Credential-owned tables. Plaintext secrets never appear in any statement:
-- the adapter seals before insert and opens after read, inside its own
-- transaction.

-- name: InsertProviderCredential :execrows
INSERT INTO workos_core.provider_credentials (
    id, owner_user_id, consumer_id, purpose, label, revision, status, nonce, ciphertext, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT DO NOTHING;

-- name: GetCredentialRequest :one
SELECT request_digest, result, created_at
FROM workos_core.credential_admin_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertCredentialRequest :execrows
INSERT INTO workos_core.credential_admin_requests (
    owner_user_id, idempotency_key, request_digest, result_version, result, created_at
) VALUES ($1, $2, $3, 1, $4, $5)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: FinalizeCredentialRequest :exec
UPDATE workos_core.credential_admin_requests
SET result = $1
WHERE owner_user_id = $2 AND idempotency_key = $3;

-- name: LockProviderCredential :one
SELECT id, owner_user_id, consumer_id, purpose, label, revision, status, nonce, ciphertext, created_at, updated_at
FROM workos_core.provider_credentials
WHERE id = $1
FOR UPDATE;

-- name: UpdateCredentialMaterial :execrows
UPDATE workos_core.provider_credentials
SET label = $1, revision = $2, nonce = $3, ciphertext = $4, updated_at = $5
WHERE id = $6;

-- name: RevokeProviderCredential :execrows
UPDATE workos_core.provider_credentials
SET status = 'revoked', revision = $1, updated_at = $2
WHERE id = $3;

-- name: ListOwnerCredentials :many
SELECT id, owner_user_id, consumer_id, purpose, label, revision, status, created_at, updated_at
FROM workos_core.provider_credentials
WHERE owner_user_id = $1
ORDER BY consumer_id, purpose, id;

-- name: GetActiveCredential :one
SELECT id, owner_user_id, consumer_id, purpose, label, revision, status, created_at, updated_at
FROM workos_core.provider_credentials
WHERE owner_user_id = $1 AND consumer_id = $2 AND purpose = $3 AND status = 'active';

-- name: GetCredentialByID :one
SELECT id, owner_user_id, consumer_id, purpose, label, revision, status, created_at, updated_at
FROM workos_core.provider_credentials
WHERE id = $1 AND owner_user_id = $2;

-- Sealed read for lease issuance inside the coordinator transaction: the
-- exact credential identity must still be active at the exact snapshot
-- revision, or the lease fails closed.
-- name: LockSealedCredentialForTask :one
SELECT id, owner_user_id, consumer_id, purpose, label, revision, status, nonce, ciphertext, created_at, updated_at
FROM workos_core.provider_credentials
WHERE id = $1 AND owner_user_id = $2
FOR UPDATE;

-- name: GetTaskCredentialLease :one
SELECT id, task_lease_id, task_id, worker_id, owner_user_id, consumer_id, purpose,
       credential_id, credential_revision, status, expires_at
FROM workos_core.task_credential_leases
WHERE task_lease_id = $1;

-- name: GetTaskCredentialLeaseByLeaseID :one
SELECT id, task_lease_id, task_id, worker_id, owner_user_id, consumer_id, purpose,
       credential_id, credential_revision, status, expires_at
FROM workos_core.task_credential_leases
WHERE id = $1;

-- name: InsertTaskCredentialLease :execrows
INSERT INTO workos_core.task_credential_leases (
    id, task_lease_id, task_id, worker_id, owner_user_id, consumer_id, purpose,
    credential_id, credential_revision, status, expires_at, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10, $11)
ON CONFLICT (task_lease_id) DO NOTHING;

-- name: RenewTaskCredentialLease :one
UPDATE workos_core.task_credential_leases
SET expires_at = $1
WHERE id = $2 AND task_lease_id = $3 AND worker_id = $4
  AND status = 'active' AND expires_at >= $5
RETURNING id, task_lease_id, task_id, worker_id, owner_user_id, consumer_id, purpose,
          credential_id, credential_revision, status, expires_at;

-- name: LockActiveTaskCredentialLease :one
SELECT id, task_lease_id, task_id, worker_id, owner_user_id, consumer_id, purpose,
       credential_id, credential_revision, status, expires_at
FROM workos_core.task_credential_leases
WHERE id = $1 AND task_lease_id = $2 AND worker_id = $3
  AND status = 'active' AND expires_at >= $4
FOR UPDATE;

-- name: ExtendTaskCredentialLease :execrows
UPDATE workos_core.task_credential_leases
SET expires_at = $1
WHERE id = $2;

-- name: ReleaseTaskCredentialLease :execrows
UPDATE workos_core.task_credential_leases
SET status = 'released', released_at = $1
WHERE id = $2 AND task_lease_id = $3 AND worker_id = $4 AND status = 'active';

-- name: ExpireStaleTaskCredentialLeases :execrows
UPDATE workos_core.task_credential_leases
SET status = 'expired'
WHERE status = 'active' AND expires_at < $1;
