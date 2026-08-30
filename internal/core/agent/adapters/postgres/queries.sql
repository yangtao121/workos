-- name: InsertAgentTask :execrows
INSERT INTO workos_core.agent_tasks (
    id, owner_user_id, idempotency_key, project_id, input, state, provider_id, created_at, updated_at,
    policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetAgentTask :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at,
       policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND id = $2;

-- name: GetAgentTaskByIdempotency :one
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at,
       policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: ListAgentTasks :many
SELECT id, owner_user_id, idempotency_key, project_id, input, state, provider_id,
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at,
       policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
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
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at,
       policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
FROM workos_core.agent_tasks
WHERE owner_user_id = $1 AND id = $2
FOR UPDATE;

-- name: MarkTaskCancelled :exec
UPDATE workos_core.agent_tasks
SET state = 'cancelled', cancellation_requested = true, last_event_sequence = $1, updated_at = $2
WHERE id = $3;

-- name: RequestTaskCancellation :exec
UPDATE workos_core.agent_tasks
SET cancellation_requested = true, updated_at = $1
WHERE id = $2;

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
       harness_instance_id, run_id, last_event_sequence, cancellation_requested, created_at, updated_at,
       policy_source, policy_revision, policy_spec_digest, budget_max_output_tokens, budget_max_runtime_seconds
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
SELECT t.id, t.owner_user_id, t.last_event_sequence, t.state, t.provider_id, t.created_at,
       t.budget_max_output_tokens
FROM workos_events.outbox AS o
JOIN workos_core.agent_tasks AS t ON t.id = o.aggregate_id
WHERE o.lease_id = $1 AND o.locked_by = $2 AND o.processed_at IS NULL AND o.locked_until >= $3
FOR UPDATE OF o, t;

-- name: LockTaskArtifactStream :one
SELECT t.id, t.owner_user_id, t.project_id, t.input, t.last_event_sequence, t.state,
       t.provider_id, t.created_at
FROM workos_events.outbox AS o
JOIN workos_core.agent_tasks AS t ON t.id = o.aggregate_id
WHERE o.lease_id = $1 AND o.locked_by = $2 AND o.processed_at IS NULL AND o.locked_until >= $3
FOR UPDATE OF o, t;

-- name: AdvanceTaskState :exec
UPDATE workos_core.agent_tasks
SET state = sqlc.arg(state),
    run_id = COALESCE(NULLIF(sqlc.arg(run_id)::text, ''), run_id),
    last_event_sequence = sqlc.arg(sequence), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(task_id);

-- name: InsertTaskEvent :exec
INSERT INTO workos_events.events (
    id, stream_type, stream_id, sequence, event_type, payload, occurred_at
) VALUES ($1, 'agent-task', $2, $3, $4, $5, $6);

-- AdvanceTaskPublicationSequence moves only the Core-minted publication
-- event's sequence forward; the task state itself never changes here.
-- name: AdvanceTaskPublicationSequence :exec
UPDATE workos_core.agent_tasks
SET last_event_sequence = sqlc.arg(sequence), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(task_id);

-- name: FinishTaskLease :execrows
UPDATE workos_events.outbox AS o
SET processed_at = $1, lease_id = NULL, locked_by = NULL, locked_until = NULL
FROM workos_core.agent_tasks AS t
WHERE o.lease_id = $2 AND o.locked_by = $3 AND t.id = o.aggregate_id
  AND t.state IN ('completed', 'failed', 'cancelled');

-- name: InsertAgentAppTaskRequest :execrows
INSERT INTO workos_core.agent_app_task_requests (
    owner_user_id, app_instance_id, client_idempotency_key, request_digest, task_id, project_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (owner_user_id, app_instance_id, client_idempotency_key) DO NOTHING;

-- name: GetAgentAppTaskRequest :one
SELECT request_digest, task_id, project_id
FROM workos_core.agent_app_task_requests
WHERE owner_user_id = $1 AND app_instance_id = $2 AND client_idempotency_key = $3;

-- name: GetAgentAppTaskByTask :one
SELECT request_digest, task_id, project_id
FROM workos_core.agent_app_task_requests
WHERE owner_user_id = $1 AND app_instance_id = $2 AND task_id = $3;

-- name: GetAgentAppTaskOwnerTask :one
SELECT app_instance_id
FROM workos_core.agent_app_task_requests
WHERE owner_user_id = $1 AND task_id = $2;

-- name: GetAgentAppPolicy :one
SELECT owner_user_id, app_instance_id, project_id, execution_mode,
       max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
       max_reserved_output_tokens_per_utc_day, spec_digest, policy_revision, created_at, updated_at
FROM workos_core.agent_app_policies
WHERE owner_user_id = $1 AND app_instance_id = $2;

-- name: LockAgentAppPolicyChain :exec
-- Serializes every transaction that reads-or-writes one installation's policy
-- chain (SetPolicy invalidation scans, waiting-approval creation). The
-- transaction-scoped advisory lock exists even when no policy row does, so a
-- first SetPolicy can never interleave between an approval-creation's policy
-- read and its pending-approval insert.
SELECT pg_advisory_xact_lock(sqlc.arg('lock_namespace')::int, sqlc.arg('lock_key')::int);

-- name: GetAgentAppPolicyForUpdate :one
SELECT owner_user_id, app_instance_id, project_id, execution_mode,
       max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
       max_reserved_output_tokens_per_utc_day, spec_digest, policy_revision, created_at, updated_at
FROM workos_core.agent_app_policies
WHERE owner_user_id = $1 AND app_instance_id = $2
FOR UPDATE;

-- name: UpsertAgentAppPolicy :one
INSERT INTO workos_core.agent_app_policies (
    owner_user_id, app_instance_id, project_id, execution_mode,
    max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
    max_reserved_output_tokens_per_utc_day, spec_digest, policy_revision, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $10
)
ON CONFLICT (owner_user_id, app_instance_id) DO UPDATE SET
    project_id = EXCLUDED.project_id,
    execution_mode = EXCLUDED.execution_mode,
    max_output_tokens_per_task = EXCLUDED.max_output_tokens_per_task,
    max_runtime_seconds_per_task = EXCLUDED.max_runtime_seconds_per_task,
    max_tasks_per_utc_day = EXCLUDED.max_tasks_per_utc_day,
    max_reserved_output_tokens_per_utc_day = EXCLUDED.max_reserved_output_tokens_per_utc_day,
    spec_digest = EXCLUDED.spec_digest,
    policy_revision = agent_app_policies.policy_revision + 1,
    updated_at = EXCLUDED.updated_at
WHERE agent_app_policies.spec_digest IS DISTINCT FROM EXCLUDED.spec_digest
  AND agent_app_policies.policy_revision = sqlc.arg('expected_revision')::bigint
RETURNING policy_revision, (xmax = 0) AS inserted;

-- name: InsertAgentAppPolicyRequest :execrows
INSERT INTO workos_core.agent_app_policy_requests (
    owner_user_id, idempotency_key, request_digest, result, created_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetAgentAppPolicyRequest :one
SELECT request_digest, result
FROM workos_core.agent_app_policy_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: UpdateAgentAppPolicyRequestResult :exec
UPDATE workos_core.agent_app_policy_requests
SET result = $1
WHERE owner_user_id = $2 AND idempotency_key = $3;

-- name: ExpirePendingApprovals :many
UPDATE workos_core.agent_app_approvals
SET state = 'expired', updated_at = $1
WHERE owner_user_id = $2 AND app_instance_id = $3 AND state = 'pending'
RETURNING id, task_id;

-- name: ExpireTaskPendingApproval :execrows
UPDATE workos_core.agent_app_approvals
SET state = 'expired', updated_at = $1
WHERE owner_user_id = $2 AND task_id = $3 AND state = 'pending';

-- name: InsertAgentAppApproval :execrows
INSERT INTO workos_core.agent_app_approvals (
    owner_user_id, id, app_instance_id, project_id, task_id, app_id, goal_excerpt, provider_id,
    max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
    max_reserved_output_tokens_per_utc_day, policy_revision, state, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'pending', $14, $14)
ON CONFLICT (owner_user_id, id) DO NOTHING;

-- name: GetAgentAppApproval :one
SELECT owner_user_id, id, app_instance_id, project_id, task_id, app_id, goal_excerpt, provider_id,
       max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
       max_reserved_output_tokens_per_utc_day, policy_revision, state,
       decided_idempotency_key, decision_digest, decided_at, created_at, updated_at
FROM workos_core.agent_app_approvals
WHERE owner_user_id = $1 AND id = $2;

-- name: GetAgentAppApprovalForUpdate :one
SELECT owner_user_id, id, app_instance_id, project_id, task_id, app_id, goal_excerpt, provider_id,
       max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
       max_reserved_output_tokens_per_utc_day, policy_revision, state,
       decided_idempotency_key, decision_digest, decided_at, created_at, updated_at
FROM workos_core.agent_app_approvals
WHERE owner_user_id = $1 AND id = $2
FOR UPDATE;

-- name: ListAgentAppApprovals :many
SELECT owner_user_id, id, app_instance_id, project_id, task_id, app_id, goal_excerpt, provider_id,
       max_output_tokens_per_task, max_runtime_seconds_per_task, max_tasks_per_utc_day,
       max_reserved_output_tokens_per_utc_day, policy_revision, state,
       decided_idempotency_key, decision_digest, decided_at, created_at, updated_at
FROM workos_core.agent_app_approvals
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND id > sqlc.arg(cursor)
  AND (sqlc.arg(project_id)::text = '' OR project_id = sqlc.arg(project_id)::uuid)
  AND (sqlc.arg(state)::text = '' OR state = sqlc.arg(state))
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: DecideAgentAppApproval :execrows
UPDATE workos_core.agent_app_approvals
SET state = sqlc.arg(state), decided_idempotency_key = sqlc.arg(idempotency_key),
    decision_digest = sqlc.arg(decision_digest), decided_at = sqlc.arg(decided_at), updated_at = sqlc.arg(decided_at)
WHERE owner_user_id = sqlc.arg(owner_user_id) AND id = sqlc.arg(approval_id) AND state = 'pending';

-- name: ReserveAgentAppDailyQuota :one
INSERT INTO workos_core.agent_app_daily_reservations (
    owner_user_id, app_instance_id, utc_date, tasks_reserved, output_tokens_reserved,
    policy_revision, created_at, updated_at
) VALUES ($1, $2, $3, 1, $4, $5, $6, $6)
ON CONFLICT (owner_user_id, app_instance_id, utc_date) DO UPDATE SET
    tasks_reserved = agent_app_daily_reservations.tasks_reserved + 1,
    output_tokens_reserved = agent_app_daily_reservations.output_tokens_reserved + EXCLUDED.output_tokens_reserved,
    policy_revision = EXCLUDED.policy_revision,
    updated_at = EXCLUDED.updated_at
WHERE agent_app_daily_reservations.tasks_reserved < sqlc.arg('max_tasks')::bigint
  AND agent_app_daily_reservations.output_tokens_reserved + EXCLUDED.output_tokens_reserved
      <= sqlc.arg('max_reserved_tokens')::bigint
  AND NOT EXISTS (
      SELECT 1 FROM workos_core.agent_app_daily_usage AS u
      WHERE u.owner_user_id = agent_app_daily_reservations.owner_user_id
        AND u.app_instance_id = agent_app_daily_reservations.app_instance_id
        AND u.utc_date = agent_app_daily_reservations.utc_date
        AND u.quota_breached
  )
RETURNING tasks_reserved, output_tokens_reserved;

-- name: GetAgentAppDailyReservations :one
SELECT tasks_reserved, output_tokens_reserved, policy_revision
FROM workos_core.agent_app_daily_reservations
WHERE owner_user_id = $1 AND app_instance_id = $2 AND utc_date = $3;

-- name: GetAgentAppDailyUsage :one
SELECT tasks_recorded, input_tokens_recorded, output_tokens_recorded,
       cost_decimal_recorded, quota_breached
FROM workos_core.agent_app_daily_usage
WHERE owner_user_id = $1 AND app_instance_id = $2 AND utc_date = $3;

-- name: UpsertAgentTaskUsage :one
INSERT INTO workos_core.agent_task_usage (
    owner_user_id, task_id, input_tokens, output_tokens, cost_decimal, model, updated_at
) VALUES ($1, $2, $3, $4, $5::numeric, $6, $7)
ON CONFLICT (owner_user_id, task_id) DO UPDATE SET
    input_tokens = agent_task_usage.input_tokens + EXCLUDED.input_tokens,
    output_tokens = agent_task_usage.output_tokens + EXCLUDED.output_tokens,
    cost_decimal = CASE
        WHEN agent_task_usage.cost_decimal IS NULL AND EXCLUDED.cost_decimal IS NULL THEN NULL
        ELSE COALESCE(agent_task_usage.cost_decimal, 0) + COALESCE(EXCLUDED.cost_decimal, 0)
    END,
    model = CASE WHEN EXCLUDED.model <> '' THEN EXCLUDED.model ELSE agent_task_usage.model END,
    updated_at = EXCLUDED.updated_at
RETURNING input_tokens, output_tokens, (xmax = 0) AS inserted;

-- name: UpsertAgentAppDailyUsage :exec
INSERT INTO workos_core.agent_app_daily_usage (
    owner_user_id, app_instance_id, utc_date, tasks_recorded,
    input_tokens_recorded, output_tokens_recorded, cost_decimal_recorded, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $8)
ON CONFLICT (owner_user_id, app_instance_id, utc_date) DO UPDATE SET
    tasks_recorded = agent_app_daily_usage.tasks_recorded + EXCLUDED.tasks_recorded,
    input_tokens_recorded = agent_app_daily_usage.input_tokens_recorded + EXCLUDED.input_tokens_recorded,
    output_tokens_recorded = agent_app_daily_usage.output_tokens_recorded + EXCLUDED.output_tokens_recorded,
    cost_decimal_recorded = CASE
        WHEN agent_app_daily_usage.cost_decimal_recorded IS NULL AND EXCLUDED.cost_decimal_recorded IS NULL THEN NULL
        ELSE COALESCE(agent_app_daily_usage.cost_decimal_recorded, 0) + COALESCE(EXCLUDED.cost_decimal_recorded, 0)
    END,
    updated_at = EXCLUDED.updated_at;

-- name: MarkAgentAppUsageBreach :exec
UPDATE workos_core.agent_app_daily_usage
SET quota_breached = true, updated_at = $1
WHERE owner_user_id = $2 AND app_instance_id = $3 AND utc_date = $4;
