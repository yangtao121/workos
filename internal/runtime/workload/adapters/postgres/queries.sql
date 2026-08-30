-- Workload Manager persistence queries (runtime-host owned tables only).

-- name: InsertWorkload :exec
INSERT INTO workos_runtime.workloads (
    id, owner_user_id, project_id, app_instance_id, app_id, app_version,
    manifest_digest, image, command, port, requested_policy, policy_version,
    effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
    effective_pids_max, effective_startup_seconds, effective_restart_limit,
    generation, state, container_name, health_verdict, last_exit_category,
    created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(project_id), sqlc.arg(app_instance_id),
    sqlc.arg(app_id), sqlc.arg(app_version), sqlc.arg(manifest_digest), sqlc.arg(image),
    sqlc.arg(command), sqlc.arg(port), sqlc.arg(requested_policy), sqlc.arg(policy_version),
    sqlc.arg(effective_cpu_quota_us), sqlc.arg(effective_memory_high_bytes),
    sqlc.arg(effective_memory_max_bytes), sqlc.arg(effective_pids_max),
    sqlc.arg(effective_startup_seconds), sqlc.arg(effective_restart_limit),
    sqlc.arg(generation), sqlc.arg(state), sqlc.arg(container_name),
    sqlc.arg(health_verdict), sqlc.arg(last_exit_category),
    sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: InsertWorkloadOperation :execrows
-- The primary key is the same-key race arbiter: a concurrent reserve of the
-- same (workload, key) inserts nothing and the caller re-reads instead.
INSERT INTO workos_runtime.workload_operations (
    workload_id, operation_key, operation, request_digest, result_generation,
    created_at, updated_at
) VALUES (sqlc.arg(workload_id), sqlc.arg(operation_key), sqlc.arg(operation),
          sqlc.arg(request_digest), sqlc.arg(result_generation),
          sqlc.arg(created_at), sqlc.arg(updated_at))
ON CONFLICT (workload_id, operation_key) DO NOTHING;

-- name: GetWorkload :one
SELECT id, owner_user_id, project_id, app_instance_id, app_id, app_version,
       manifest_digest, image, command, port, requested_policy, policy_version,
       effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
       effective_pids_max, effective_startup_seconds, effective_restart_limit,
       generation, state, restart_count, container_id, container_name, endpoint,
       cgroup_path, health_verdict, last_exit_category,
       baseline_memory_events_oom, baseline_pids_events_peak,
       last_verified_at, lease_owner, lease_expires_at,
       created_at, updated_at, started_at, stopped_at, idle_since
FROM workos_runtime.workloads
WHERE id = sqlc.arg(id);

-- name: GetActiveWorkloadByInstance :one
SELECT id, owner_user_id, project_id, app_instance_id, app_id, app_version,
       manifest_digest, image, command, port, requested_policy, policy_version,
       effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
       effective_pids_max, effective_startup_seconds, effective_restart_limit,
       generation, state, restart_count, container_id, container_name, endpoint,
       cgroup_path, health_verdict, last_exit_category,
       baseline_memory_events_oom, baseline_pids_events_peak,
       last_verified_at, lease_owner, lease_expires_at,
       created_at, updated_at, started_at, stopped_at, idle_since
FROM workos_runtime.workloads
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_instance_id = sqlc.arg(app_instance_id)
  AND state NOT IN ('stopped', 'failed')
ORDER BY created_at, id
LIMIT 1;

-- name: ListWorkloads :many
SELECT id, owner_user_id, project_id, app_instance_id, app_id, app_version,
       manifest_digest, image, command, port, requested_policy, policy_version,
       effective_cpu_quota_us, effective_memory_high_bytes, effective_memory_max_bytes,
       effective_pids_max, effective_startup_seconds, effective_restart_limit,
       generation, state, restart_count, container_id, container_name, endpoint,
       cgroup_path, health_verdict, last_exit_category,
       baseline_memory_events_oom, baseline_pids_events_peak,
       last_verified_at, lease_owner, lease_expires_at,
       created_at, updated_at, started_at, stopped_at, idle_since
FROM workos_runtime.workloads
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: GetWorkloadOperation :one
SELECT workload_id, operation_key, operation, request_digest,
       result_state, result_generation, error_kind, created_at, updated_at
FROM workos_runtime.workload_operations
WHERE workload_id = sqlc.arg(workload_id) AND operation_key = sqlc.arg(operation_key);

-- name: GetPendingWorkloadOperation :one
-- Recovers the original command that owns a starting generation. The target
-- generation is persisted before engine side effects, so reconcile never
-- invents a second action key and a replay cannot advance twice.
SELECT workload_id, operation_key, operation, request_digest,
       result_state, result_generation, error_kind, created_at, updated_at
FROM workos_runtime.workload_operations
WHERE workload_id = sqlc.arg(workload_id)
  AND result_generation = sqlc.arg(generation)
  AND result_state IS NULL
  AND (error_kind IS NULL OR error_kind IN ('unavailable', 'failed'))
ORDER BY updated_at DESC, operation_key
LIMIT 1;

-- name: UpsertWorkloadOperation :execrows
-- Records or finalizes one operation verdict; the request_digest never
-- changes after the first reserve. Terminal verdicts are immutable: only an
-- open/retryable row may converge to a later result.
INSERT INTO workos_runtime.workload_operations (
    workload_id, operation_key, operation, request_digest,
    result_state, result_generation, error_kind, created_at, updated_at
) VALUES (
    sqlc.arg(workload_id), sqlc.arg(operation_key), sqlc.arg(operation),
    sqlc.arg(request_digest), sqlc.arg(result_state), sqlc.arg(result_generation),
    sqlc.arg(error_kind), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (workload_id, operation_key) DO UPDATE
SET result_state = sqlc.arg(result_state),
    result_generation = sqlc.arg(result_generation),
    error_kind = sqlc.arg(error_kind),
    updated_at = sqlc.arg(updated_at)
WHERE workos_runtime.workload_operations.result_state IS NULL
  AND workos_runtime.workload_operations.operation = sqlc.arg(operation)
  AND workos_runtime.workload_operations.request_digest = sqlc.arg(request_digest)
  AND (workos_runtime.workload_operations.error_kind IS NULL
       OR workos_runtime.workload_operations.error_kind IN ('unavailable', 'failed'));

-- name: SetWorkloadRunning :execrows
-- The generation guard keeps a stale launch from landing on top of a newer
-- restart: only the exact starting generation may become running.
UPDATE workos_runtime.workloads SET
    state = 'running',
    container_id = sqlc.arg(container_id),
    endpoint = sqlc.arg(endpoint),
    cgroup_path = sqlc.arg(cgroup_path),
    health_verdict = sqlc.arg(health_verdict),
    last_exit_category = sqlc.arg(last_exit_category),
    baseline_memory_events_oom = sqlc.arg(baseline_memory_events_oom),
    baseline_pids_events_peak = sqlc.arg(baseline_pids_events_peak),
    idle_since = NULL,
    started_at = sqlc.arg(started_at),
    stopped_at = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND state = 'starting' AND generation = sqlc.arg(generation);

-- name: RestartWorkloadFrom :execrows
-- The guarded restart transition: re-open a running or failed workload under
-- generation+1, clear the engine facts, and restart the count.
UPDATE workos_runtime.workloads SET
    state = 'starting',
    generation = sqlc.arg(generation),
    restart_count = sqlc.arg(restart_count),
    container_id = NULL,
    endpoint = NULL,
    cgroup_path = NULL,
    health_verdict = 'unknown',
    last_exit_category = 'none',
    baseline_memory_events_oom = 0,
    baseline_pids_events_peak = 0,
    idle_since = NULL,
    last_verified_at = NULL,
    started_at = NULL,
    stopped_at = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND state = sqlc.arg(from_state)
  AND state IN ('running', 'failed')
  AND generation + 1 = sqlc.arg(generation)
  AND restart_count + 1 = sqlc.arg(restart_count);

-- name: SetWorkloadState :execrows
-- Generic guarded transition for stopping/failed/stopped landings; the
-- caller supplies the bounded fact bundle explicitly. The generation guard
-- prevents a stale reconcile observation from mutating a replacement that
-- has already reached the same lifecycle state.
UPDATE workos_runtime.workloads SET
    state = sqlc.arg(to_state),
    container_id = sqlc.arg(container_id),
    endpoint = sqlc.arg(endpoint),
    cgroup_path = sqlc.arg(cgroup_path),
    health_verdict = sqlc.arg(health_verdict),
    last_exit_category = sqlc.arg(last_exit_category),
    idle_since = NULL,
    stopped_at = sqlc.arg(stopped_at),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND state = sqlc.arg(from_state)
  AND generation = sqlc.arg(generation)
  AND state NOT IN ('stopped', 'failed');

-- name: StampWorkloadVerified :execrows
-- Bookkeeping only: the verification stamp anchors the Core-grace clock and
-- deliberately does NOT touch lifecycle timestamps or idle_since.
UPDATE workos_runtime.workloads SET
    last_verified_at = sqlc.arg(verified_at)
WHERE id = sqlc.arg(id) AND state = 'running'
  AND generation = sqlc.arg(generation);

-- name: ClaimWorkloadLease :execrows
-- Bookkeeping only: claiming the reconcile lease deliberately does NOT touch
-- lifecycle timestamps or idle_since — observation is not active use.
UPDATE workos_runtime.workloads SET
    lease_owner = sqlc.arg(lease_owner),
    lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id)
  AND (lease_owner = sqlc.arg(lease_owner)
       OR lease_expires_at IS NULL
       OR lease_expires_at < sqlc.arg(now));

-- name: MarkWorkloadIdle :one
-- Idle bookkeeping belongs to the exact running generation observed by the
-- caller; an old reconcile pass may not start the next generation's clock.
UPDATE workos_runtime.workloads SET
    idle_since = COALESCE(idle_since, sqlc.arg(idle_since))
WHERE id = sqlc.arg(id) AND state = 'running'
  AND generation = sqlc.arg(generation)
RETURNING idle_since;

-- name: ClearWorkloadIdle :execrows
-- The same generation guard applies when active use clears the clock.
UPDATE workos_runtime.workloads SET idle_since = NULL
WHERE id = sqlc.arg(id) AND state = 'running'
  AND generation = sqlc.arg(generation)
  AND idle_since IS NOT NULL;
