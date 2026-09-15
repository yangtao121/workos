-- Owner: core project workspace bindings (ADR-0030). Core owns ownership and
-- authorization facts; the runtime owns directory assembly. Clients reference
-- operator-registered workspace sources and never submit host paths.
CREATE TABLE workos_core.project_workspace_bindings (
    binding_id uuid PRIMARY KEY CHECK (binding_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    workspace_source_id text NOT NULL CHECK (length(workspace_source_id) BETWEEN 1 AND 128),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 128),
    read_only boolean NOT NULL DEFAULT false,
    state text NOT NULL CHECK (state IN ('active', 'archived')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    archived_at timestamptz,
    UNIQUE (owner_user_id, idempotency_key)
);

-- One active development binding per project in this phase.
CREATE UNIQUE INDEX project_workspace_one_active_idx
    ON workos_core.project_workspace_bindings (project_id)
    WHERE state = 'active';
