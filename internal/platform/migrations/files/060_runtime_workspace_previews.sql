-- 060: runtime-owned workspace dev previews (ADR-0030 B08).
--
-- One durable preview fact per bounded serving registration: the preview_id
-- is the URL path segment of the same-origin /previews/<id>/ route, the
-- (owner, project) scope matches the operator-registered workspace mount,
-- and the state grammar is fixed to running|stopped|expired. The serving
-- route re-validates liveness (state running AND expires_at in the future)
-- on every request, so a stopped or TTL-expired row always answers 404.
--
-- Idempotency is per LIVE key: the partial unique index arbitrates only
-- running rows — an identical (owner, idempotency_key) start replays the
-- running preview, while an expired or stopped row never blocks a fresh
-- registration. Races are arbitrated by ON CONFLICT DO NOTHING in the
-- adapter, never by process state.

CREATE TABLE workos_runtime.workspace_previews (
    preview_id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    workspace_source_id text NOT NULL CHECK (char_length(workspace_source_id) BETWEEN 1 AND 64),
    read_only boolean NOT NULL,
    state text NOT NULL CHECK (state IN ('running', 'stopped', 'expired')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);

-- Only one live preview per (owner, idempotency key): expired/stopped rows
-- deliberately fall outside the arbiter so a later start can register fresh.
CREATE UNIQUE INDEX workspace_previews_owner_key_live_unique
    ON workos_runtime.workspace_previews (owner_user_id, idempotency_key)
    WHERE state = 'running';

CREATE INDEX workspace_previews_project_state_idx
    ON workos_runtime.workspace_previews (project_id, state);
