-- Owner: runtime-host supervised virtual-display native sessions (ADR-0029).
-- Durable owner-scoped native sessions: idempotent creation, fixed initial
-- display size, idle expiry.
CREATE TABLE workos_runtime.native_sessions (
    session_id uuid PRIMARY KEY CHECK (session_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'closed', 'failed')),
    width integer NOT NULL CHECK (width BETWEEN 320 AND 1920),
    height integer NOT NULL CHECK (height BETWEEN 240 AND 1200),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX native_sessions_reconcile_idx
    ON workos_runtime.native_sessions (state, updated_at);
