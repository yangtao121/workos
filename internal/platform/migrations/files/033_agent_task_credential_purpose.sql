-- 033_agent_task_credential_purpose.sql
-- Owner: workos-core Agent (internal/core/agent), same table as 024.
--
-- ADR-0015: the per-task credential snapshot now pins the canonical
-- credential purpose (kind) the provider requires, so lease derivation
-- opens exactly the snapshotted kind even after the finite vocabulary
-- expands beyond provider-api-key.v1. Existing rows were all created under
-- provider-api-key.v1 and are backfilled before the column becomes
-- mandatory.

ALTER TABLE workos_core.agent_task_credentials
    ADD COLUMN purpose text;

UPDATE workos_core.agent_task_credentials
SET purpose = 'provider-api-key.v1'
WHERE purpose IS NULL;

ALTER TABLE workos_core.agent_task_credentials
    ALTER COLUMN purpose SET NOT NULL,
    ADD CONSTRAINT agent_task_credentials_purpose_vocabulary
        CHECK (purpose IN (
            'provider-api-key.v1', 'codex-auth.v1', 'github-token.v1', 'cloud-credential.v1'));

COMMENT ON COLUMN workos_core.agent_task_credentials.purpose IS
    'owner: workos-core Agent; exact credential kind pinned at admission (ADR-0015)';
