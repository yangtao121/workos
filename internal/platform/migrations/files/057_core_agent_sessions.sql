-- Owner: core continuous harness sessions and their durable inputs
-- (ADR-0030). One session binds a project, a workspace binding revision, a
-- provider/profile snapshot, and one native harness session reference.
-- Inputs are idempotent per (session, client_input_id) and ordered by a
-- strictly increasing per-session sequence.
CREATE TABLE workos_core.agent_sessions (
    session_id uuid PRIMARY KEY CHECK (session_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    workspace_binding_id uuid,
    workspace_binding_revision bigint NOT NULL DEFAULT 0,
    provider_id text NOT NULL CHECK (length(provider_id) BETWEEN 1 AND 64),
    profile_id text NOT NULL DEFAULT '' CHECK (length(profile_id) <= 128),
    state text NOT NULL CHECK (state IN ('active', 'closing', 'closed')),
    native_session_ref text NOT NULL DEFAULT '' CHECK (length(native_session_ref) <= 256),
    active_task_id uuid,
    input_sequence bigint NOT NULL DEFAULT 0 CHECK (input_sequence >= 0),
    event_sequence bigint NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX agent_sessions_project_idx
    ON workos_core.agent_sessions (project_id, state, updated_at DESC);

CREATE TABLE workos_core.agent_session_inputs (
    input_id uuid PRIMARY KEY CHECK (input_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    session_id uuid NOT NULL REFERENCES workos_core.agent_sessions (session_id),
    owner_user_id uuid NOT NULL,
    client_input_id text NOT NULL CHECK (length(client_input_id) BETWEEN 1 AND 128),
    input_text text NOT NULL CHECK (length(input_text) BETWEEN 1 AND 65536),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('accepted', 'dispatched', 'completed', 'failed', 'cancelled')),
    task_id uuid,
    sequence bigint NOT NULL CHECK (sequence > 0),
    result_summary text NOT NULL DEFAULT '' CHECK (length(result_summary) <= 2048),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (session_id, client_input_id),
    UNIQUE (session_id, sequence)
);

CREATE INDEX agent_session_inputs_pending_idx
    ON workos_core.agent_session_inputs (session_id, sequence)
    WHERE state IN ('accepted', 'dispatched');

-- Session lifecycle log: bounded projection for display, audit, and cursor
-- based catch-up. Assistant/tool/usage projections stay on task events.
CREATE TABLE workos_core.agent_session_events (
    session_id uuid NOT NULL REFERENCES workos_core.agent_sessions (session_id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    event_type text NOT NULL CHECK (event_type IN
        ('input_accepted', 'input_dispatched', 'input_terminal', 'state_changed')),
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, sequence)
);
