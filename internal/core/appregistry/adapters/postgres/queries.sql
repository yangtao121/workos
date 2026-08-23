-- name: InsertAppVersion :execrows
INSERT INTO workos_core.app_versions (
    id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
    permissions, manifest_digest, canonical_manifest, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
ON CONFLICT DO NOTHING;

-- name: GetAppVersion :one
SELECT id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
       permissions, manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE owner_user_id = $1 AND app_id = $2 AND version = $3;

-- name: GetAppVersionByIdempotency :one
SELECT id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
       permissions, manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: GetAppVersions :many
SELECT id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
       permissions, manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE owner_user_id = sqlc.arg(owner_user_id) AND app_id = sqlc.arg(app_id)
ORDER BY created_at, id;

-- name: ListAppIDs :many
SELECT DISTINCT app_id
FROM workos_core.app_versions
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_id > sqlc.arg(cursor)
ORDER BY app_id
LIMIT sqlc.arg(row_limit);

-- name: ListAppVersionsForApps :many
SELECT id, owner_user_id, idempotency_key, request_digest, app_id, version, scope, name,
       permissions, manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_id = ANY(sqlc.arg(app_ids)::text[])
ORDER BY app_id, created_at, id;
