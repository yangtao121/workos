-- 015: runtime-host supervised workloads and web-service surface sessions
-- (owner: runtime-host Workload Manager; ADR-0006).
--
-- The runtime owns the durable Workload identity: one row per supervised
-- container the runtime launched for an installed instance, carrying the
-- effective (server-adjudicated) resource/health policy, the generation, the
-- engine container identity, the server-verified loopback endpoint, and the
-- engine-inspected cgroup path. The rows never reference Core-owned tables:
-- liveness of the installation is re-validated through the private Core
-- resolver, never via a join into another module's schema.
--
--   workloads            durable Workload facts (one active per owner+instance,
--                        enforced by a partial unique index over non-terminal
--                        states);
--   workload_operations  durable idempotency authority for ensure/restart/
--                        terminate: same key + same canonical command replays
--                        the first result across process restarts.
--
-- surface_sessions evolves additively for the web-service renderer: the
-- renderer CHECK becomes a two-value grammar with renderer-specific mutually
-- exclusive facts (web-bundle rows keep their artifact columns; web-service
-- rows reference the workload identity and generation they were opened
-- against). Legacy web-bundle rows keep byte-identical semantics.

CREATE TABLE workos_runtime.workloads (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    app_instance_id uuid NOT NULL,
    app_id text NOT NULL CHECK (app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    app_version text NOT NULL CHECK (app_version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    image text NOT NULL CHECK (image ~ '^[^@]+@sha256:[0-9a-f]{64}$'),
    command jsonb NOT NULL CHECK (jsonb_typeof(command) = 'array' AND jsonb_array_length(command) BETWEEN 1 AND 16),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    requested_policy jsonb NOT NULL,
    policy_version text NOT NULL CHECK (policy_version = 'v1'),
    effective_cpu_quota_us bigint NOT NULL CHECK (effective_cpu_quota_us BETWEEN 10000 AND 400000),
    effective_memory_high_bytes bigint NOT NULL CHECK (effective_memory_high_bytes BETWEEN 16777216 AND 1073741824),
    effective_memory_max_bytes bigint NOT NULL CHECK (effective_memory_max_bytes BETWEEN 33554432 AND 2147483648),
    effective_pids_max integer NOT NULL CHECK (effective_pids_max BETWEEN 8 AND 512),
    effective_startup_seconds integer NOT NULL CHECK (effective_startup_seconds BETWEEN 1 AND 120),
    effective_restart_limit integer NOT NULL CHECK (effective_restart_limit BETWEEN 0 AND 8),
    generation bigint NOT NULL CHECK (generation >= 1),
    state text NOT NULL CHECK (state IN ('pending', 'starting', 'running', 'stopping', 'stopped', 'failed')),
    restart_count integer NOT NULL DEFAULT 0 CHECK (restart_count >= 0),
    container_id text,
    container_name text NOT NULL CHECK (container_name ~ '^workos-wl-[0-9a-f-]{36}$'),
    endpoint text CHECK (endpoint IS NULL OR endpoint ~ '^127\.0\.0\.1:[1-9][0-9]{0,4}$'),
    cgroup_path text,
    health_verdict text NOT NULL DEFAULT 'unknown' CHECK (health_verdict IN ('unknown', 'ok', 'failing')),
    last_exit_category text NOT NULL DEFAULT 'none' CHECK (last_exit_category IN ('none', 'exited', 'oom', 'pids', 'unknown')),
    baseline_memory_events_oom bigint NOT NULL DEFAULT 0 CHECK (baseline_memory_events_oom >= 0),
    baseline_pids_events_peak bigint NOT NULL DEFAULT 0 CHECK (baseline_pids_events_peak >= 0),
    last_verified_at timestamptz,
    lease_owner text,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    started_at timestamptz,
    stopped_at timestamptz,
    CONSTRAINT workloads_running_facts CHECK (
        state <> 'running'
        OR (container_id IS NOT NULL AND endpoint IS NOT NULL AND cgroup_path IS NOT NULL AND started_at IS NOT NULL)
    ),
    CONSTRAINT workloads_terminal_facts CHECK (
        state NOT IN ('stopped', 'failed')
        OR (container_id IS NULL AND endpoint IS NULL AND cgroup_path IS NULL AND stopped_at IS NOT NULL)
    ),
    CONSTRAINT workloads_coherent_generation CHECK (
        started_at IS NULL OR started_at >= created_at
    )
);

-- At most one active workload per (owner, installed instance). Stopped and
-- failed workloads are historical facts and do not block a fresh ensure.
CREATE UNIQUE INDEX workloads_active_instance_idx
    ON workos_runtime.workloads (owner_user_id, app_instance_id)
    WHERE state NOT IN ('stopped', 'failed');

CREATE INDEX workloads_owner_instance_idx
    ON workos_runtime.workloads (owner_user_id, app_instance_id);

CREATE TABLE workos_runtime.workload_operations (
    workload_id uuid NOT NULL,
    operation_key text NOT NULL CHECK (char_length(operation_key) BETWEEN 1 AND 128),
    operation text NOT NULL CHECK (operation IN ('ensure', 'restart', 'terminate')),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result_state text CHECK (result_state IN ('starting', 'running', 'stopped', 'failed')),
    result_generation bigint CHECK (result_generation IS NULL OR result_generation >= 1),
    error_kind text CHECK (error_kind IN ('invalid', 'unsupported', 'conflict', 'limit_exhausted', 'unavailable', 'failed')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (workload_id, operation_key),
    CONSTRAINT workload_operations_owner_fkey
        FOREIGN KEY (workload_id)
        REFERENCES workos_runtime.workloads (id)
        ON DELETE RESTRICT
);

ALTER TABLE workos_runtime.surface_sessions
    DROP CONSTRAINT surface_sessions_renderer_check,
    ALTER COLUMN artifact_id DROP NOT NULL,
    ALTER COLUMN artifact_digest DROP NOT NULL,
    ALTER COLUMN entrypoint DROP NOT NULL,
    ADD COLUMN workload_id uuid,
    ADD COLUMN workload_generation bigint,
    ADD CONSTRAINT surface_sessions_renderer_check CHECK (renderer IN ('web-bundle', 'web-service')),
    ADD CONSTRAINT surface_sessions_renderer_coherence CHECK (
        (
            renderer = 'web-bundle'
            AND artifact_id IS NOT NULL AND artifact_digest IS NOT NULL AND entrypoint IS NOT NULL
            AND workload_id IS NULL AND workload_generation IS NULL
        )
        OR (
            renderer = 'web-service'
            AND artifact_id IS NULL AND artifact_digest IS NULL AND entrypoint IS NULL
            AND workload_id IS NOT NULL AND workload_generation IS NOT NULL AND workload_generation >= 1
        )
    ),
    ADD CONSTRAINT surface_sessions_workload_fkey
        FOREIGN KEY (workload_id)
        REFERENCES workos_runtime.workloads (id)
        ON DELETE RESTRICT;
