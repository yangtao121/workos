-- 034_task_credential_lease_purpose_vocabulary.sql
-- Owner: workos-core Credential Vault (internal/core/credential, ADR-0015).
--
-- Migration 032 widened provider_credentials.purpose to the finite
-- credential-kind vocabulary; the short task_credential_leases rows (023)
-- carry the same purpose fact and need the identical widening, otherwise a
-- codex-auth.v1 lease derivation violates the stored CHECK at acquire time.

DO $$
DECLARE
    purpose_constraint text;
BEGIN
    SELECT conname INTO purpose_constraint
    FROM pg_constraint
    WHERE conrelid = 'workos_core.task_credential_leases'::regclass
      AND contype = 'c'
      AND conkey = ARRAY[(
          SELECT attnum::smallint FROM pg_attribute
          WHERE attrelid = 'workos_core.task_credential_leases'::regclass
            AND attname = 'purpose'
            AND NOT attisdropped)]
    ORDER BY conname
    LIMIT 1;
    IF purpose_constraint IS NULL THEN
        RAISE EXCEPTION 'task_credential_leases purpose CHECK constraint not found';
    END IF;
    EXECUTE format('ALTER TABLE workos_core.task_credential_leases DROP CONSTRAINT %I', purpose_constraint);
END $$;

ALTER TABLE workos_core.task_credential_leases
    ADD CONSTRAINT task_credential_leases_purpose_vocabulary
        CHECK (purpose IN (
            'provider-api-key.v1', 'codex-auth.v1', 'github-token.v1', 'cloud-credential.v1'));
