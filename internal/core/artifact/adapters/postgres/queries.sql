-- name: InsertArtifact :exec
INSERT INTO workos_core.web_bundle_artifacts (
    id, owner_user_id, type, title, media_type, content_ref, digest,
    entrypoint, file_count, total_size_bytes, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: InsertBundleFile :exec
INSERT INTO workos_core.web_bundle_files (
    artifact_id, path, media_type, size_bytes, digest, content
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetArtifactRequest :one
SELECT owner_user_id, idempotency_key, request_digest, artifact_id, created_at
FROM workos_core.web_bundle_artifact_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertArtifactRequest :execrows
INSERT INTO workos_core.web_bundle_artifact_requests (
    owner_user_id, idempotency_key, request_digest, artifact_id, created_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: GetArtifact :one
SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
       entrypoint, file_count, total_size_bytes, created_at
FROM workos_core.web_bundle_artifacts
WHERE owner_user_id = $1 AND id = $2;

-- name: ListArtifactIDPage :many
SELECT id
FROM workos_core.web_bundle_artifacts
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.arg(cursor) = '' OR id > sqlc.arg(cursor)::uuid)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListArtifactSummaries :many
SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
       entrypoint, file_count, total_size_bytes, created_at
FROM workos_core.web_bundle_artifacts
WHERE owner_user_id = sqlc.arg(owner_user_id) AND id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id;

-- name: ReadBundleAsset :one
SELECT f.path, f.media_type, f.size_bytes, f.digest, f.content
FROM workos_core.web_bundle_files f
JOIN workos_core.web_bundle_artifacts a ON a.id = f.artifact_id
WHERE a.owner_user_id = $1 AND a.id = $2 AND f.path = $3;
