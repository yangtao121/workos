-- name: GetProjectHarnessBinding :one
SELECT harness_binding
FROM workos_core.projects
WHERE id = $1 AND owner_user_id = $2 AND archived_at IS NULL;

-- name: InsertAgentTask :execrows
INSERT INTO workos_core.agent_tasks (
    id, owner_user_id, idempotency_key, project_id, input, state, provider_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetAgentTask :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND id = $2;

-- name: GetAgentTaskByIdempotency :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: ListAgentTasks :many
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at
FROM workos_core.agent_tasks
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.arg(project_id)::text = '' OR project_id = sqlc.arg(project_id)::uuid)
  AND id > sqlc.arg(cursor)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: InsertTaskOutbox :exec
INSERT INTO workos_events.outbox (
    id, aggregate_type, aggregate_id, event_type, payload, occurred_at
) VALUES ($1, 'agent-task', $2, 'agent.task.requested.v1', $3, $4);

-- name: GetAgentTaskForUpdate :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND id = $2
FOR UPDATE;

-- name: MarkTaskCancelled :exec
UPDATE workos_core.agent_tasks
SET state = 'cancelled', cancellation_requested = true, last_event_sequence = $1, updated_at = $2
WHERE id = $3;

-- name: FinishPendingTaskRequest :exec
UPDATE workos_events.outbox
SET processed_at = $1
WHERE aggregate_type = 'agent-task' AND aggregate_id = $2 AND processed_at IS NULL;

-- name: TaskBelongsToOwner :one
SELECT EXISTS(
    SELECT 1 FROM workos_core.agent_tasks WHERE id = $1 AND owner_user_id = $2
) AS allowed;

-- name: ListTaskEvents :many
SELECT id, stream_id, sequence, event_type, payload, occurred_at
FROM workos_events.events
WHERE stream_type = 'agent-task' AND stream_id = $1 AND sequence > $2
ORDER BY sequence
LIMIT $3;

-- name: SelectTaskClaim :one
SELECT o.aggregate_id
FROM workos_events.outbox AS o
JOIN workos_core.agent_tasks AS t ON t.id = o.aggregate_id
WHERE o.event_type = 'agent.task.requested.v1' AND o.processed_at IS NULL
  AND (o.locked_until IS NULL OR o.locked_until < $1)
  AND t.state IN ('queued', 'running', 'waiting')
ORDER BY o.occurred_at
FOR UPDATE OF o SKIP LOCKED
LIMIT 1;

-- name: LeaseTask :exec
UPDATE workos_events.outbox
SET lease_id = $1, locked_by = $2, locked_until = $3, attempts = attempts + 1
WHERE event_type = 'agent.task.requested.v1' AND aggregate_id = $4 AND processed_at IS NULL;

-- name: MarkTaskRunning :exec
UPDATE workos_core.agent_tasks SET state = 'running', updated_at = $1 WHERE id = $2;

-- name: GetAgentTaskUnscoped :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at
FROM workos_core.agent_tasks
WHERE id = $1;

-- name: RenewTaskLease :one
UPDATE workos_events.outbox AS o
SET locked_until = sqlc.arg(expires_at)
FROM workos_core.agent_tasks AS t
WHERE o.lease_id = sqlc.arg(lease_id) AND o.locked_by = sqlc.arg(worker_id)
  AND o.processed_at IS NULL AND o.locked_until >= sqlc.arg(observed_at)
  AND t.id = o.aggregate_id
RETURNING t.cancellation_requested;

-- name: LockTaskEventStream :one
SELECT t.id, t.last_event_sequence, t.state
FROM workos_events.outbox AS o
JOIN workos_core.agent_tasks AS t ON t.id = o.aggregate_id
WHERE o.lease_id = $1 AND o.locked_by = $2 AND o.processed_at IS NULL AND o.locked_until >= $3
FOR UPDATE OF o, t;

-- name: AdvanceTaskState :exec
UPDATE workos_core.agent_tasks
SET state = sqlc.arg(state),
    provider_id = COALESCE(NULLIF(sqlc.arg(provider_id)::text, ''), provider_id),
    run_id = COALESCE(NULLIF(sqlc.arg(run_id)::text, ''), run_id),
    last_event_sequence = sqlc.arg(sequence), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(task_id);

-- name: InsertTaskEvent :exec
INSERT INTO workos_events.events (
    id, stream_type, stream_id, sequence, event_type, payload, occurred_at
) VALUES ($1, 'agent-task', $2, $3, $4, $5, $6);

-- name: FinishTaskLease :execrows
UPDATE workos_events.outbox AS o
SET processed_at = $1, lease_id = NULL, locked_by = NULL, locked_until = NULL
FROM workos_core.agent_tasks AS t
WHERE o.lease_id = $2 AND o.locked_by = $3 AND t.id = o.aggregate_id
  AND t.state IN ('completed', 'failed', 'cancelled');
