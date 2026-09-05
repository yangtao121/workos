-- 036_reliability_deployment_ledger.sql
-- Owner: reliability-host (internal/reliability, ADR-0016 section 6).
--
-- Deployment controller ledger: one row per incident-scoped canary. The
-- canary START transitions the installation to the candidate version
-- (ADR-0012 semantics, driven by the controller over the loopback); a calm
-- observation window promotes, a fresh incident for the same installation
-- rolls back to the previous pinned version.

CREATE TABLE workos_reliability.deployment_ledger (
    incident_id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    target_version text NOT NULL CHECK (char_length(target_version) BETWEEN 1 AND 64),
    state text NOT NULL CHECK (state IN ('canary', 'promoted', 'rolled_back', 'failed')),
    canary_until timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at),
    CHECK (canary_until < 'infinity' AND canary_until > '-infinity'),
    CHECK (created_at < 'infinity' AND created_at > '-infinity'),
    CHECK (updated_at < 'infinity' AND updated_at > '-infinity')
);

CREATE INDEX deployment_ledger_state_idx ON workos_reliability.deployment_ledger (state, canary_until);
