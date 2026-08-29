-- 014: App Agent policy, pre-run approval, quota reservation, and usage
-- projection (owner: workos-core Agent; ADR-0005).
--
-- The installation grant answers which bridge methods an App may call; it does
-- not answer how much a task may cost or whether each run needs user approval.
-- These facts belong to the Agent module, keyed by (owner, app instance) and
-- pinned to a project snapshot ID — deliberately NO cross-module foreign key
-- into Project/Installation tables: liveness is re-validated through ports/RPC
-- on every mutation, never via a join into another module's schema.
--
--   agent_app_policies            explicit per-installation policy (full
--                                 replacement spec + own revision epoch);
--   agent_app_policy_requests     owner-scoped idempotency authority for
--                                 SetAppPolicy with versioned first-response
--                                 snapshot (ADR-0004 pattern);
--   agent_app_approvals           pre-run approval facts (pending until an
--                                 owner decision or policy invalidation);
--   agent_app_daily_reservations enqueue-time quota buckets (UTC daily,
--                                 guarded increments, no refunds);
--   agent_app_daily_usage         observed usage projection per bucket
--                                 (authorization is reservation, not usage);
--   agent_task_usage              cumulative observed usage per task.
--
-- agent_tasks gains additive, NULLable budget/policy snapshot columns: tasks
-- created before 014 have no policy history and it is not fabricable — they
-- stay NULL (legacy/unknown) instead of being backfilled as "billed 0".
-- App tasks created after 014 always snapshot the effective policy source and
-- the server-derived budget at creation time.

ALTER TABLE workos_core.agent_tasks
    ADD COLUMN policy_source text,
    ADD COLUMN policy_revision bigint,
    ADD COLUMN policy_spec_digest text,
    ADD COLUMN budget_max_output_tokens bigint,
    ADD COLUMN budget_max_runtime_seconds bigint;

ALTER TABLE workos_core.agent_tasks
    ADD CONSTRAINT agent_tasks_policy_snapshot_check CHECK ((
        policy_source IS NULL AND policy_revision IS NULL
            AND policy_spec_digest IS NULL
            AND budget_max_output_tokens IS NULL
            AND budget_max_runtime_seconds IS NULL
    ) OR (
        policy_source IN ('system_default', 'explicit')
            AND policy_revision > 0
            AND policy_spec_digest ~ '^sha256:[0-9a-f]{64}$'
            AND budget_max_output_tokens > 0
            AND budget_max_runtime_seconds > 0
    ));

CREATE TABLE workos_core.agent_app_policies (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    app_instance_id uuid NOT NULL,
    project_id uuid NOT NULL,
    execution_mode text NOT NULL CHECK (execution_mode IN ('allow', 'require_approval', 'block')),
    max_output_tokens_per_task bigint NOT NULL CHECK (max_output_tokens_per_task BETWEEN 1 AND 1000000),
    max_runtime_seconds_per_task bigint NOT NULL CHECK (max_runtime_seconds_per_task BETWEEN 1 AND 86400),
    max_tasks_per_utc_day bigint NOT NULL CHECK (max_tasks_per_utc_day BETWEEN 1 AND 10000),
    max_reserved_output_tokens_per_utc_day bigint NOT NULL CHECK (max_reserved_output_tokens_per_utc_day BETWEEN 1 AND 10000000),
    spec_digest text NOT NULL CHECK (spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, app_instance_id),
    CONSTRAINT agent_app_policies_daily_at_least_per_task_check
        CHECK (max_reserved_output_tokens_per_utc_day >= max_output_tokens_per_task)
);

CREATE TABLE workos_core.agent_app_policy_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object' AND result -> 'result_version' IS NOT DISTINCT FROM '"1"'::jsonb),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);

CREATE TABLE workos_core.agent_app_approvals (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    id uuid NOT NULL,
    app_instance_id uuid NOT NULL,
    project_id uuid NOT NULL,
    task_id uuid NOT NULL,
    app_id text NOT NULL CHECK (char_length(app_id) BETWEEN 1 AND 128),
    goal_excerpt text NOT NULL CHECK (char_length(goal_excerpt) BETWEEN 1 AND 512),
    provider_id text NOT NULL CHECK (char_length(provider_id) BETWEEN 1 AND 128),
    -- Full policy spec snapshot: a later decision reserves and enqueues
    -- against exactly the approved numbers, never the current policy.
    max_output_tokens_per_task bigint NOT NULL CHECK (max_output_tokens_per_task > 0),
    max_runtime_seconds_per_task bigint NOT NULL CHECK (max_runtime_seconds_per_task > 0),
    max_tasks_per_utc_day bigint NOT NULL CHECK (max_tasks_per_utc_day > 0),
    max_reserved_output_tokens_per_utc_day bigint NOT NULL CHECK (max_reserved_output_tokens_per_utc_day > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    state text NOT NULL CHECK (state IN ('pending', 'approved', 'rejected', 'expired')),
    decided_idempotency_key text CHECK (decided_idempotency_key IS NULL OR char_length(decided_idempotency_key) BETWEEN 1 AND 128),
    decision_digest text CHECK (decision_digest IS NULL OR decision_digest ~ '^sha256:[0-9a-f]{64}$'),
    decided_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, id),
    CONSTRAINT agent_app_approvals_owner_task_fkey
        FOREIGN KEY (owner_user_id, task_id)
        REFERENCES workos_core.agent_tasks (owner_user_id, id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_app_approvals_decided_shape_check CHECK ((
        state = 'pending' AND decided_idempotency_key IS NULL AND decision_digest IS NULL AND decided_at IS NULL
    ) OR (
        state IN ('approved', 'rejected') AND decided_idempotency_key IS NOT NULL AND decided_at IS NOT NULL
    ) OR (
        state = 'expired' AND decided_idempotency_key IS NULL AND decided_at IS NULL
    ))
);

-- Deterministic listing order for the owner's approvals.
CREATE INDEX agent_app_approvals_owner_idx
    ON workos_core.agent_app_approvals (owner_user_id, id);

CREATE TABLE workos_core.agent_app_daily_reservations (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    app_instance_id uuid NOT NULL,
    utc_date date NOT NULL,
    tasks_reserved bigint NOT NULL CHECK (tasks_reserved >= 0),
    output_tokens_reserved bigint NOT NULL CHECK (output_tokens_reserved >= 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, app_instance_id, utc_date)
);

CREATE TABLE workos_core.agent_app_daily_usage (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    app_instance_id uuid NOT NULL,
    utc_date date NOT NULL,
    tasks_recorded bigint NOT NULL DEFAULT 0 CHECK (tasks_recorded >= 0),
    input_tokens_recorded bigint NOT NULL DEFAULT 0 CHECK (input_tokens_recorded >= 0),
    output_tokens_recorded bigint NOT NULL DEFAULT 0 CHECK (output_tokens_recorded >= 0),
    cost_decimal_recorded numeric,
    quota_breached boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, app_instance_id, utc_date)
);

CREATE TABLE workos_core.agent_task_usage (
    owner_user_id uuid NOT NULL,
    task_id uuid NOT NULL,
    input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_decimal numeric,
    model text NOT NULL DEFAULT '' CHECK (char_length(model) <= 128),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, task_id),
    CONSTRAINT agent_task_usage_owner_task_fkey
        FOREIGN KEY (owner_user_id, task_id)
        REFERENCES workos_core.agent_tasks (owner_user_id, id)
        ON DELETE RESTRICT
);

-- Reverse lookup from an App task to its installation for usage projection.
CREATE INDEX agent_app_task_requests_owner_task_idx
    ON workos_core.agent_app_task_requests (owner_user_id, task_id);
