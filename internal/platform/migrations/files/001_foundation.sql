CREATE SCHEMA IF NOT EXISTS workos_core;
CREATE SCHEMA IF NOT EXISTS workos_events;

CREATE TABLE workos_core.users (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind = 'owner'),
    display_name text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX users_single_owner_idx ON workos_core.users (kind);

CREATE TABLE workos_core.devices (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES workos_core.users (id),
    name text NOT NULL,
    public_key text,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE workos_core.projects (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    icon text NOT NULL DEFAULT '',
    workspace_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    harness_binding jsonb,
    installed_app_ids text[] NOT NULL DEFAULT '{}',
    default_agent_role text NOT NULL DEFAULT '',
    knowledge_collection_id uuid NOT NULL,
    artifact_collection_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    archived_at timestamptz,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX projects_owner_active_idx
    ON workos_core.projects (owner_user_id, id)
    WHERE archived_at IS NULL;

CREATE TABLE workos_core.agent_tasks (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL,
    project_id uuid REFERENCES workos_core.projects (id),
    input jsonb NOT NULL,
    state text NOT NULL CHECK (state IN ('queued', 'running', 'waiting', 'completed', 'failed', 'cancelled')),
    provider_id text NOT NULL,
    harness_instance_id text NOT NULL DEFAULT '',
    run_id text NOT NULL DEFAULT '',
    last_event_sequence bigint NOT NULL DEFAULT 0 CHECK (last_event_sequence >= 0),
    cancellation_requested boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX agent_tasks_project_idx
    ON workos_core.agent_tasks (owner_user_id, project_id, id);

CREATE TABLE workos_events.events (
    id uuid PRIMARY KEY,
    stream_type text NOT NULL,
    stream_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    UNIQUE (stream_type, stream_id, sequence)
);

CREATE INDEX events_stream_cursor_idx
    ON workos_events.events (stream_type, stream_id, sequence);

CREATE TABLE workos_events.outbox (
    id uuid PRIMARY KEY,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    lease_id uuid,
    locked_by text,
    locked_until timestamptz,
    processed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0
);

CREATE INDEX outbox_claim_idx
    ON workos_events.outbox (event_type, occurred_at)
    WHERE processed_at IS NULL;

CREATE TABLE workos_events.consumer_cursors (
    consumer_id text NOT NULL,
    stream_type text NOT NULL,
    stream_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (consumer_id, stream_type, stream_id)
);
