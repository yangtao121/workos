-- Owner: workos-core Agent (ADR-0009). The durable per-task credential
-- snapshot: the exact credential ID and revision a fresh task was admitted
-- with, resolved before any queue row, outbox entry, reservation, or waiting
-- approval exists. The snapshot binds history — replay, approval decisions,
-- and credential acquires verify against this exact pair and never silently
-- adopt a rotated or rebound credential. It references only the Agent-owned
-- task row; it deliberately has no foreign key into the Credential module's
-- tables (validated through the neutral port at run time).

CREATE TABLE workos_core.agent_task_credentials (
    task_id uuid PRIMARY KEY REFERENCES workos_core.agent_tasks (id) ON DELETE CASCADE,
    provider_id text NOT NULL,
    credential_id uuid NOT NULL,
    credential_revision bigint NOT NULL CHECK (credential_revision >= 1),
    created_at timestamptz NOT NULL,
    CHECK (created_at < 'infinity' AND created_at > '-infinity')
);
