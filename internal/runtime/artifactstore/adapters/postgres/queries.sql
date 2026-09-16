-- name: InsertArtifactReady :one
INSERT INTO workos_runtime.artifacts (
    id, owner_user_id, digest, format, size_bytes, file_count, state, origin,
    idempotency_key, app_id, task_id, job_id, incident_id, project_id,
    installation_id, source_bundle_id, source_digest, manifest_digest,
    base_image, build_command, test_command, output_directory,
    created_at, ready_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, sqlc.arg(state), $7,
    $8, $9, $10, $11, $12, $13,
    $14, $15, $16, $17,
    $18, $19, $20, $21,
    $22, sqlc.narg(ready_at), $22
)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetArtifactById :one
SELECT * FROM workos_runtime.artifacts
WHERE id = $1;

-- name: GetArtifactByOwnerDigest :one
SELECT * FROM workos_runtime.artifacts
WHERE owner_user_id = $1 AND digest = $2
ORDER BY created_at, id LIMIT 1;

-- name: GetArtifactByTask :one
SELECT * FROM workos_runtime.artifacts
WHERE task_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetArtifactByImportKey :one
SELECT * FROM workos_runtime.artifacts
WHERE owner_user_id = $1 AND origin = 'operator_import' AND idempotency_key = $2;

-- name: MarkArtifactState :execrows
UPDATE workos_runtime.artifacts
SET state = $2, updated_at = $3, ready_at = CASE WHEN $2 = 'ready' THEN COALESCE(ready_at, $3) ELSE ready_at END
WHERE id = $1;

-- name: SumOwnerArtifactBytes :one
SELECT COALESCE(SUM(size_bytes), 0)::bigint AS usage_bytes
FROM workos_runtime.artifacts
WHERE owner_user_id = $1
  AND state IN ('preparing', 'ready');

-- name: ListArtifactsByState :many
SELECT * FROM workos_runtime.artifacts
WHERE state = $1
ORDER BY created_at;
