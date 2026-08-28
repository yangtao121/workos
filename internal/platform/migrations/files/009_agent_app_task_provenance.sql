-- 009: App task provenance and request-digest idempotency (owner: workos-core Agent).
--
-- The existing user-scoped (owner_user_id, idempotency_key) uniqueness on
-- agent_tasks cannot express App submission safety: two different app
-- installations of the same owner may legitimately reuse the same client key.
-- This table is the Agent-owned durable fact answering, per bridge-created
-- task: which owner, which app installation, which project, which canonical
-- request created it, and what the first response was.
--   * primary key (owner_user_id, app_instance_id, client_idempotency_key)
--     namespaces app client keys per installation — two apps with the same
--     client key never collide;
--   * request_digest pins the canonical bounded input (sha256, versioned
--     format minted by the Agent application service): same key + same digest
--     replays the first task exactly, same key + different digest aborts;
--   * the composite foreign key binds every mapping to a task of the same
--     owner (the new agent_tasks owner/id unique constraint), so a cross-
--     owner result mapping is impossible at the storage layer;
--   * app_instance_id and project_id are stable snapshot IDs only: there is
--     deliberately NO cross-module foreign key into Project/Registry tables —
--     liveness of the installation is re-validated through ports/RPC on every
--     bridge call, never via a join into another module's schema.

ALTER TABLE workos_core.agent_tasks
    ADD CONSTRAINT agent_tasks_owner_id_unique UNIQUE (owner_user_id, id);

CREATE TABLE workos_core.agent_app_task_requests (
    owner_user_id uuid NOT NULL,
    app_instance_id uuid NOT NULL,
    client_idempotency_key text NOT NULL CHECK (char_length(client_idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    task_id uuid NOT NULL,
    project_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, app_instance_id, client_idempotency_key),
    CONSTRAINT agent_app_task_requests_owner_task_fkey
        FOREIGN KEY (owner_user_id, task_id)
        REFERENCES workos_core.agent_tasks (owner_user_id, id)
        ON DELETE RESTRICT
);

CREATE INDEX agent_app_task_requests_task_idx
    ON workos_core.agent_app_task_requests (task_id);
