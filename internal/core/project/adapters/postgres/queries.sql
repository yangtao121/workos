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

-- name: GetInstallationRequest :one
SELECT owner_user_id, idempotency_key, command, request_digest, installation_id,
       project_revision, result_uninstalled_at, result_granted_permissions,
       result_grant_revision, created_at
FROM workos_core.project_app_installation_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertInstallationRequest :execrows
INSERT INTO workos_core.project_app_installation_requests (
    owner_user_id, idempotency_key, command, request_digest, installation_id,
    project_revision, result_uninstalled_at, result_granted_permissions,
    result_grant_revision, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: LockProjectForInstallation :one
SELECT id, owner_user_id, revision, archived_at
FROM workos_core.projects
WHERE owner_user_id = $1 AND id = $2
FOR UPDATE;

-- name: GetActiveInstallationByApp :one
SELECT id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, grant_revision, installed_at, uninstalled_at
FROM workos_core.project_app_installations
WHERE project_id = $1 AND app_id = $2 AND uninstalled_at IS NULL;

-- name: GetInstallationById :one
SELECT id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, grant_revision, installed_at, uninstalled_at
FROM workos_core.project_app_installations
WHERE owner_user_id = $1 AND id = $2;

-- name: ResolveActiveInstallation :one
SELECT i.id, i.owner_user_id, i.project_id, i.app_id, i.version, i.manifest_digest, i.granted_permissions, i.grant_revision, i.installed_at, i.uninstalled_at
FROM workos_core.project_app_installations i
JOIN workos_core.projects p
  ON p.id = i.project_id AND p.owner_user_id = i.owner_user_id AND p.archived_at IS NULL
WHERE i.owner_user_id = sqlc.arg(owner_user_id)
  AND i.project_id = sqlc.arg(project_id)
  AND i.id = sqlc.arg(id)
  AND i.uninstalled_at IS NULL;

-- name: SetInstallationGrants :one
UPDATE workos_core.project_app_installations
SET granted_permissions = sqlc.arg(granted_permissions),
    grant_revision = grant_revision + 1
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND uninstalled_at IS NULL
RETURNING id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, grant_revision, installed_at, uninstalled_at;

-- name: InsertInstallation :exec
INSERT INTO workos_core.project_app_installations (
    id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, installed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: TombstoneInstallation :execrows
UPDATE workos_core.project_app_installations
SET uninstalled_at = sqlc.arg(uninstalled_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND project_id = sqlc.arg(project_id)
  AND id = sqlc.arg(id)
  AND uninstalled_at IS NULL;

-- name: ActiveInstallationAppIDs :one
SELECT coalesce(array_agg(app_id ORDER BY app_id), '{}'::text[])::text[] AS app_ids
FROM workos_core.project_app_installations
WHERE project_id = sqlc.arg(project_id) AND uninstalled_at IS NULL;

-- name: ApplyInstallationProjection :one
UPDATE workos_core.projects
SET revision = revision + 1,
    updated_at = sqlc.arg(updated_at),
    installed_app_ids = sqlc.arg(installed_app_ids)
WHERE id = sqlc.arg(id)
  AND owner_user_id = sqlc.arg(owner_user_id)
  AND revision = sqlc.arg(expected_revision)
  AND archived_at IS NULL
RETURNING revision, updated_at;

-- name: GetCreateRequest :one
SELECT owner_user_id, idempotency_key, request_digest, result, created_at
FROM workos_core.project_create_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertCreateRequest :execrows
INSERT INTO workos_core.project_create_requests (
    owner_user_id, idempotency_key, request_digest, result, created_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: ListActiveInstallations :many
SELECT id, owner_user_id, project_id, app_id, version, manifest_digest, granted_permissions, grant_revision, installed_at, uninstalled_at
FROM workos_core.project_app_installations
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND project_id = sqlc.arg(project_id)
  AND app_id > sqlc.arg(cursor)
  AND uninstalled_at IS NULL
ORDER BY app_id
LIMIT sqlc.arg(row_limit);
