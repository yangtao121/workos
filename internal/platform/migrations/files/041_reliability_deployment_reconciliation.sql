-- Owner: reliability-host. Preserve prior evidence, but never replay an
-- old observation-only canary as a real version deployment.
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN expected_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    ADD COLUMN canary_started_at timestamptz NOT NULL DEFAULT now();
UPDATE workos_reliability.deployment_ledger SET state = 'failed'
WHERE state = 'canary';
ALTER TABLE workos_reliability.deployment_ledger
    DROP CONSTRAINT deployment_ledger_state_check,
    ADD CONSTRAINT deployment_ledger_state_check
        CHECK (state IN ('candidate', 'starting', 'canary', 'rollback', 'promoted', 'rolled_back', 'failed')),
    ADD CONSTRAINT deployment_live_revision_check
        CHECK (state NOT IN ('candidate', 'starting', 'canary', 'rollback') OR expected_revision > 0);
