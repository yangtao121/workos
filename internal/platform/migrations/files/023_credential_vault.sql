-- Owner: workos-core Credential Vault (ADR-0009). These tables are the only
-- durable authority for long-lived provider credential material. Plaintext
-- secrets, master keys, and child environment values never appear in any
-- column: the vault stores only AEAD nonce + ciphertext pairs produced by the
-- Core process master key, plus non-secret metadata and versioned keyed
-- idempotency digests.

CREATE TABLE workos_core.provider_credentials (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    consumer_id text NOT NULL CHECK (consumer_id ~ '^[a-z0-9._-]{1,128}$'),
    purpose text NOT NULL CHECK (purpose = 'provider-api-key.v1'),
    label text NOT NULL DEFAULT '' CHECK (label = '' OR (label ~ '^[^[:cntrl:]]{1,80}$')),
    revision bigint NOT NULL CHECK (revision >= 1),
    status text NOT NULL CHECK (status IN ('active', 'revoked')),
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 12),
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) > 16),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (created_at < 'infinity' AND created_at > '-infinity'),
    CHECK (updated_at < 'infinity' AND updated_at > '-infinity'),
    CHECK (updated_at >= created_at)
);

-- At most one active credential per (owner, consumer, purpose). Rotation and
-- revocation keep the logical row and bump its revision; a new Put after a
-- revoke creates a new ID and never revives the old binding.
CREATE UNIQUE INDEX provider_credentials_active_idx
    ON workos_core.provider_credentials (owner_user_id, consumer_id, purpose)
    WHERE status = 'active';

CREATE INDEX provider_credentials_owner_idx
    ON workos_core.provider_credentials (owner_user_id, id);

-- Durable admin idempotency: the request digest is a versioned keyed digest
-- (HMAC, key derived from the Core master key) so a leaked database cannot
-- verify guesses of the secret offline. The first response snapshot is
-- persisted metadata so same-key/same-request replays are exact across
-- restarts; failed transactions never consume a key.
CREATE TABLE workos_core.credential_admin_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^workos.credential-request.v1:[0-9a-f]{64}$'),
    result_version integer NOT NULL CHECK (result_version = 1),
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);

-- Short task-bound credential leases. Lease rows never contain secret
-- material; the decrypted value exists only in the Acquire response inside
-- Core/harness process memory. There is deliberately no foreign key to the
-- Agent module's lease tables: the application coordinator validates the
-- active task lease through the neutral port inside the same transaction.
CREATE TABLE workos_core.task_credential_leases (
    id uuid PRIMARY KEY,
    task_lease_id uuid NOT NULL,
    task_id uuid NOT NULL,
    worker_id text NOT NULL CHECK (octet_length(worker_id) BETWEEN 1 AND 128),
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    consumer_id text NOT NULL CHECK (consumer_id ~ '^[a-z0-9._-]{1,128}$'),
    purpose text NOT NULL CHECK (purpose = 'provider-api-key.v1'),
    credential_id uuid NOT NULL REFERENCES workos_core.provider_credentials (id),
    credential_revision bigint NOT NULL CHECK (credential_revision >= 1),
    status text NOT NULL CHECK (status IN ('active', 'released', 'expired')),
    expires_at timestamptz NOT NULL,
    released_at timestamptz,
    created_at timestamptz NOT NULL,
    CHECK (expires_at < 'infinity' AND expires_at > '-infinity'),
    CHECK (released_at IS NULL OR (released_at < 'infinity' AND released_at > '-infinity')),
    CHECK (created_at < 'infinity' AND created_at > '-infinity'),
    CHECK (
        (status = 'released' AND released_at IS NOT NULL)
        OR (status <> 'released' AND released_at IS NULL)
    )
);

-- A response-loss replay of Acquire for the same active task lease returns
-- the same lease row and the same credential revision — never a second row.
CREATE UNIQUE INDEX task_credential_leases_task_lease_idx
    ON workos_core.task_credential_leases (task_lease_id);

CREATE INDEX task_credential_leases_expiry_idx
    ON workos_core.task_credential_leases (status, expires_at);
