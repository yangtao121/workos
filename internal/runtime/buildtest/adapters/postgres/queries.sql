-- name: InsertBuildJob :execrows
INSERT INTO workos_runtime.build_jobs (
    id, task_id, incident_id, owner_user_id, project_id, installation_id,
    input_digest, source_bundle_id, source_digest, manifest_digest, base_image,
    payload, state, stage, attempts, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'queued', 'submit', 0, $13, $13)
ON CONFLICT (task_id) DO NOTHING;

-- name: GetBuildJobByTask :one
SELECT id, task_id, incident_id, owner_user_id, project_id, installation_id,
       input_digest, source_bundle_id, source_digest, manifest_digest, base_image,
       payload, state, stage, build_exit_code, test_exit_code, failure_reason,
       engine_facts, attempts, log_tail, lease_owner, lease_until, created_at, updated_at
FROM workos_runtime.build_jobs
WHERE task_id = $1;

-- name: ListRunnableBuildJobs :many
SELECT id, task_id, incident_id, owner_user_id, project_id, installation_id,
       input_digest, source_bundle_id, source_digest, manifest_digest, base_image,
       payload, state, stage, build_exit_code, test_exit_code, failure_reason,
       engine_facts, attempts, log_tail, lease_owner, lease_until, created_at, updated_at
FROM workos_runtime.build_jobs
WHERE state = 'queued'
   OR (state = 'running' AND lease_until IS NOT NULL AND lease_until < sqlc.arg(now))
ORDER BY updated_at
LIMIT sqlc.arg(row_limit);

-- name: ClaimBuildJob :execrows
UPDATE workos_runtime.build_jobs
SET state = 'running', stage = 'materialize', attempts = attempts + 1,
    lease_owner = sqlc.arg(lease_owner), lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
  AND (state = 'queued'
       OR (state = 'running' AND lease_until IS NOT NULL AND lease_until < sqlc.arg(now)));

-- name: RecordBuildVerdict :execrows
UPDATE workos_runtime.build_jobs
SET state = sqlc.arg(state), stage = sqlc.arg(stage),
    build_exit_code = sqlc.arg(build_exit_code),
    test_exit_code = sqlc.arg(test_exit_code),
    failure_reason = sqlc.arg(failure_reason),
    engine_facts = sqlc.arg(engine_facts),
    log_tail = sqlc.arg(log_tail),
    lease_owner = NULL, lease_until = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner) AND state = 'running';

-- name: RequeueBuildJob :execrows
UPDATE workos_runtime.build_jobs
SET state = 'queued', stage = sqlc.arg(stage),
    lease_owner = NULL, lease_until = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner) AND state = 'running';

-- name: FailBuildJob :execrows
UPDATE workos_runtime.build_jobs
SET state = 'failed', stage = sqlc.arg(stage), failure_reason = 'engine-failed',
    lease_owner = NULL, lease_until = NULL, updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND lease_owner = sqlc.arg(lease_owner) AND state = 'running';

-- name: CancelBuildJob :execrows
UPDATE workos_runtime.build_jobs
SET state = 'cancelled', failure_reason = 'cancelled',
    lease_owner = NULL, lease_until = NULL, updated_at = sqlc.arg(now)
WHERE task_id = sqlc.arg(task_id) AND state IN ('queued', 'running');
