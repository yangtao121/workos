-- 035_reliability_repair_ledger.sql
-- Owner: reliability-host (internal/reliability, ADR-0016 section 5).
--
-- Durable repair-orchestrator ledger: one row per repair attempt of an
-- incident. The row is the idempotency anchor (one repair task per incident)
-- and the terminal-state record the orchestrator reconciles.

CREATE TABLE workos_reliability.repair_ledger (
    incident_id uuid PRIMARY KEY,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('submitted', 'terminal')),
    attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (updated_at >= created_at),
    CHECK (created_at < 'infinity' AND created_at > '-infinity'),
    CHECK (updated_at < 'infinity' AND updated_at > '-infinity')
);

CREATE INDEX repair_ledger_state_idx ON workos_reliability.repair_ledger (state, updated_at);

ALTER TABLE workos_reliability.incidents
    ADD COLUMN repair_task_id uuid;

COMMENT ON COLUMN workos_reliability.incidents.repair_task_id IS
    'owner: reliability-host; Agent repair task produced by the orchestrator (ADR-0016)';
