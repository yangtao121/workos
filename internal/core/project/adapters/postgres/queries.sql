-- name: InsertProject :execrows
INSERT INTO workos_core.projects (
    id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
    installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
    revision, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetProject :one
SELECT id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
       installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
       revision, created_at, updated_at, archived_at
FROM workos_core.projects
WHERE owner_user_id = $1 AND id = $2;

-- name: GetProjectByIdempotency :one
SELECT id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
       installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
       revision, created_at, updated_at, archived_at
FROM workos_core.projects
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: ListProjects :many
SELECT id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
       installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
       revision, created_at, updated_at, archived_at
FROM workos_core.projects
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND id > sqlc.arg(cursor)
  AND (sqlc.arg(include_archived)::boolean OR archived_at IS NULL)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: UpdateProject :one
UPDATE workos_core.projects
SET name = sqlc.arg(name), icon = sqlc.arg(icon), workspace_refs = sqlc.arg(workspace_refs),
    harness_binding = sqlc.narg(harness_binding), revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id)
  AND revision = sqlc.arg(expected_revision) AND archived_at IS NULL
RETURNING id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
          installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
          revision, created_at, updated_at, archived_at;

-- name: ArchiveProject :one
UPDATE workos_core.projects
SET archived_at = sqlc.arg(archived_at), updated_at = sqlc.arg(archived_at), revision = revision + 1
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id)
  AND revision = sqlc.arg(expected_revision) AND archived_at IS NULL
RETURNING id, owner_user_id, idempotency_key, name, icon, workspace_refs, harness_binding,
          installed_app_ids, default_agent_role, knowledge_collection_id, artifact_collection_id,
          revision, created_at, updated_at, archived_at;

-- name: InsertProjectEvent :exec
INSERT INTO workos_events.events (
    id, stream_type, stream_id, sequence, event_type, payload, occurred_at
) VALUES ($1, 'project', $2, $3, $4, $5, $6);

-- name: InsertProjectOutbox :exec
INSERT INTO workos_events.outbox (
    id, aggregate_type, aggregate_id, event_type, payload, occurred_at
) VALUES ($1, 'project', $2, $3, $4, $5);
