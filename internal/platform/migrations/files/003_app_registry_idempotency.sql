-- 003: authoritative registration-request idempotency mapping.
-- app_versions keeps only the immutable manifest fact; (owner, idempotency
-- key) semantics move to app_registration_requests so every successful key is
-- persisted exactly once and never becomes a second ruling fact source.

CREATE TABLE workos_core.app_registration_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    app_version_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);

ALTER TABLE workos_core.app_versions
    ADD CONSTRAINT app_versions_owner_id_unique UNIQUE (owner_user_id, id);

ALTER TABLE workos_core.app_registration_requests
    ADD CONSTRAINT app_registration_requests_owner_version_fkey
    FOREIGN KEY (owner_user_id, app_version_id)
    REFERENCES workos_core.app_versions (owner_user_id, id)
    ON DELETE RESTRICT;

-- Backfill the mapping from the pre-003 app_versions rows so existing volumes
-- and empty databases converge on the same facts.
INSERT INTO workos_core.app_registration_requests (
    owner_user_id, idempotency_key, request_digest, app_version_id, created_at
)
SELECT owner_user_id, idempotency_key, request_digest, id, created_at
FROM workos_core.app_versions
ON CONFLICT DO NOTHING;

-- The legacy columns and their unique constraint are removed so the mapping
-- table is the single idempotency authority.
ALTER TABLE workos_core.app_versions
    DROP CONSTRAINT app_versions_owner_user_id_idempotency_key_key,
    DROP COLUMN idempotency_key,
    DROP COLUMN request_digest;
