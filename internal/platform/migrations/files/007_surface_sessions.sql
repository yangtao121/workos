-- 007: runtime-host surface sessions (owner: runtime-host Surface Broker).
-- The surface session tables are owned by runtime-host only. They persist
-- owner/device-bound session facts, their durable create-command idempotency,
-- and the immutable launch descriptor snapshot returned by the Core resolver.
-- They never reference or query Core-owned tables: every asset request
-- revalidates the active installation through the private Core resolver.

CREATE SCHEMA IF NOT EXISTS workos_runtime;

CREATE TABLE workos_runtime.surface_sessions (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    project_id uuid NOT NULL,
    app_instance_id uuid NOT NULL,
    renderer text NOT NULL CHECK (renderer = 'web-bundle'),
    app_id text NOT NULL CHECK (app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    app_version text NOT NULL CHECK (app_version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_id uuid NOT NULL,
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    entrypoint text NOT NULL CHECK (char_length(entrypoint) BETWEEN 1 AND 240),
    path text NOT NULL CHECK (path ~ '^/surfaces/[0-9a-f-]{36}/$'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    closed_at timestamptz,
    CONSTRAINT surface_sessions_owner_id_unique UNIQUE (owner_user_id, id),
    CONSTRAINT surface_sessions_coherent_times CHECK (
        expires_at > created_at
        AND (closed_at IS NULL OR closed_at >= created_at)
    )
);

CREATE INDEX surface_sessions_owner_device_idx
    ON workos_runtime.surface_sessions (owner_user_id, device_id, id);

-- Authoritative create-command idempotency: one persisted session per
-- (owner, idempotency key), bound to the referenced session's owner.
CREATE TABLE workos_runtime.surface_session_requests (
    owner_user_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    session_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key),
    CONSTRAINT surface_session_requests_owner_session_fkey
        FOREIGN KEY (owner_user_id, session_id)
        REFERENCES workos_runtime.surface_sessions (owner_user_id, id)
        ON DELETE RESTRICT
);
