-- 068_reliability_deployment_artifacts.sql
-- Owner: reliability-host (ADR-0033). The deployment ledger snapshots the
-- exact artifact identity of the candidate and of the pinned base version at
-- offer time, and gains the honest terminal states the P3 controller needs:
-- superseded (the user changed versions; automation must never override) and
-- rollback_pending (Core pin restored but the old service is not yet proven
-- healthy — rolled_back is only written after real verification).
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN artifact_id uuid,
    ADD COLUMN artifact_digest text
        CHECK (artifact_digest IS NULL OR artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN base_artifact_digest text
        CHECK (base_artifact_digest IS NULL OR base_artifact_digest ~ '^sha256:[0-9a-f]{64}$');

ALTER TABLE workos_reliability.deployment_ledger
    DROP CONSTRAINT deployment_ledger_state_check,
    ADD CONSTRAINT deployment_ledger_state_check
        CHECK (state IN ('candidate', 'starting', 'canary', 'rollback', 'rollback_pending',
                         'promoted', 'rolled_back', 'superseded', 'failed'));
