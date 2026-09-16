-- Owner: core Agent. Durable rotation for session dispatch/finalization repair.
ALTER TABLE workos_core.agent_sessions
    ADD COLUMN recovery_checked_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00';
CREATE INDEX agent_sessions_recovery_idx ON workos_core.agent_sessions (recovery_checked_at, session_id);

ALTER TABLE workos_core.agent_sessions DROP CONSTRAINT agent_sessions_state_check;
ALTER TABLE workos_core.agent_sessions ADD CONSTRAINT agent_sessions_state_check
 CHECK (state IN ('active', 'closing', 'closed', 'needs_review'));
