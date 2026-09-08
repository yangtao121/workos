-- Owner: runtime-host Remote Browser Pool (ADR-0027). Durable owner-scoped
-- browser sessions: idempotent creation, bounded restarts, idle expiry.
CREATE TABLE workos_runtime.browser_sessions (
    session_id uuid PRIMARY KEY CHECK (session_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'restarting', 'closed', 'failed')),
    current_url text NOT NULL CHECK (char_length(current_url) BETWEEN 0 AND 2048) DEFAULT '',
    restart_count int4 NOT NULL DEFAULT 0 CHECK (restart_count >= 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX browser_sessions_reconcile_idx
    ON workos_runtime.browser_sessions (state, updated_at);
