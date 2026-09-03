-- 032_credential_vault_expansion.sql
-- Owner: workos-core Credential Vault (internal/core/credential, ADR-0015).
--
-- Forward-only expansion on top of ADR-0009 (023/024, both unchanged):
--   1. purpose becomes the finite credential-kind vocabulary. The original
--      unnamed single-column CHECK only allowed 'provider-api-key.v1'; it is
--      replaced by a named CHECK over the exact finite set.
--   2. key_epoch on every credential row supports master-key online rotation
--      (epoch-mixed key derivation). Existing rows stay at epoch 1, whose
--      derived keys are byte-identical to the pre-rotation derivation, so
--      nothing stored before this migration needs re-sealing.
--   3. credential_vault_state is the singleton authoritative epoch.
--   4. credential_admin_audit is the append-only operator audit trail for
--      put/rotate/revoke/reveal/rotate-master-key. It never stores secret
--      material of any kind.

DO $$
DECLARE
    purpose_constraint text;
BEGIN
    SELECT conname INTO purpose_constraint
    FROM pg_constraint
    WHERE conrelid = 'workos_core.provider_credentials'::regclass
      AND contype = 'c'
      AND conkey = ARRAY[(
          SELECT attnum::smallint FROM pg_attribute
          WHERE attrelid = 'workos_core.provider_credentials'::regclass
            AND attname = 'purpose'
            AND NOT attisdropped)]
    ORDER BY conname
    LIMIT 1;
    IF purpose_constraint IS NULL THEN
        RAISE EXCEPTION 'provider_credentials purpose CHECK constraint not found';
    END IF;
    EXECUTE format('ALTER TABLE workos_core.provider_credentials DROP CONSTRAINT %I', purpose_constraint);
END $$;

ALTER TABLE workos_core.provider_credentials
    ADD CONSTRAINT provider_credentials_purpose_vocabulary
        CHECK (purpose IN (
            'provider-api-key.v1', 'codex-auth.v1', 'github-token.v1', 'cloud-credential.v1')),
    ADD COLUMN key_epoch bigint NOT NULL DEFAULT 1 CHECK (key_epoch >= 1);

COMMENT ON COLUMN workos_core.provider_credentials.key_epoch IS
    'owner: workos-core Credential Vault; master-key epoch this row was sealed under (ADR-0015)';

CREATE TABLE workos_core.credential_vault_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    current_epoch bigint NOT NULL CHECK (current_epoch >= 1),
    updated_at timestamptz NOT NULL,
    CHECK (updated_at < 'infinity' AND updated_at > '-infinity')
);

INSERT INTO workos_core.credential_vault_state (singleton, current_epoch, updated_at)
VALUES (true, 1, now())
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE workos_core.credential_admin_audit (
    id uuid PRIMARY KEY,
    occurred_at timestamptz NOT NULL CHECK (occurred_at < 'infinity' AND occurred_at > '-infinity'),
    action text NOT NULL CHECK (action IN (
        'put', 'rotate', 'revoke', 'reveal', 'rotate-master-key')),
    owner_user_id uuid REFERENCES workos_core.users (id),
    credential_id uuid,
    consumer_id text CHECK (consumer_id IS NULL OR consumer_id ~ '^[a-z0-9._-]{1,128}$'),
    purpose text CHECK (purpose IS NULL OR purpose IN (
        'provider-api-key.v1', 'codex-auth.v1', 'github-token.v1', 'cloud-credential.v1')),
    revision bigint CHECK (revision IS NULL OR revision >= 1),
    key_epoch bigint CHECK (key_epoch IS NULL OR key_epoch >= 1),
    result text NOT NULL CHECK (octet_length(result) BETWEEN 1 AND 64)
);

CREATE INDEX credential_admin_audit_occurred_idx
    ON workos_core.credential_admin_audit (occurred_at, id);

COMMENT ON TABLE workos_core.credential_admin_audit IS
    'owner: workos-core Credential Vault; append-only operator audit for admin actions; never stores secret material (ADR-0015)';
