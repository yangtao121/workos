-- name: InsertAppVersion :execrows
INSERT INTO workos_core.app_versions (
    id, owner_user_id, app_id, version, scope, name, permissions,
    manifest_digest, canonical_manifest, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING;

-- name: GetAppVersion :one
SELECT id, owner_user_id, app_id, version, scope, name, permissions,
       manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE owner_user_id = $1 AND app_id = $2 AND version = $3;

-- name: GetAppVersionByID :one
SELECT id, owner_user_id, app_id, version, scope, name, permissions,
       manifest_digest, canonical_manifest, created_at
FROM workos_core.app_versions
WHERE id = $1;

-- name: GetRegistrationRequest :one
SELECT owner_user_id, idempotency_key, request_digest, app_version_id, created_at
FROM workos_core.app_registration_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertRegistrationRequest :execrows
INSERT INTO workos_core.app_registration_requests (
    owner_user_id, idempotency_key, request_digest, app_version_id, created_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: ListAppIDPage :many
SELECT DISTINCT app_id
FROM workos_core.app_versions
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_id > sqlc.arg(cursor)
ORDER BY app_id
LIMIT sqlc.arg(row_limit);

-- name: InsertAppSourceBundle :execrows
INSERT INTO workos_core.app_source_bundles (
    id, owner_user_id, idempotency_key, digest, files, total_size_bytes, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetAppSourceBundle :one
SELECT id, owner_user_id, idempotency_key, digest,
       CASE WHEN octet_length(files::text) <= 1048576 THEN files ELSE NULL::jsonb END AS files,
       total_size_bytes, created_at
FROM workos_core.app_source_bundles
WHERE owner_user_id = $1 AND id = $2;

-- name: GetAppSourceBundleByKey :one
SELECT id, owner_user_id, idempotency_key, digest,
       CASE WHEN octet_length(files::text) <= 1048576 THEN files ELSE NULL::jsonb END AS files,
       total_size_bytes, created_at
FROM workos_core.app_source_bundles
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: GetAppBuildManifest :one
SELECT manifest_digest,
       CASE WHEN octet_length(canonical_manifest::text) <= 524288
            THEN canonical_manifest ELSE NULL::jsonb END AS canonical_manifest
FROM workos_core.app_versions
WHERE owner_user_id = $1 AND app_id = $2 AND version = $3;

-- name: InsertRepairSourceCandidate :exec
INSERT INTO workos_core.app_repair_source_candidates (task_id, owner_user_id, source_bundle_id)
VALUES ($1, $2, $3);

-- name: GetRepairSourceCandidate :one
SELECT source_bundle_id
FROM workos_core.app_repair_source_candidates
WHERE task_id = $1 AND owner_user_id = $2;

-- name: FindRepairCandidateVersion :one
SELECT task_id, owner_user_id, project_id, installation_id, incident_id, build_job_id,
       source_digest, app_version_id, published_at, created_at
FROM workos_core.app_repair_candidate_versions
WHERE task_id = $1;

-- name: GetAppVersionByIDAnyState :one
SELECT owner_user_id, app_id, version, scope, name, permissions, manifest_digest, state
FROM workos_core.app_versions
WHERE id = $1;

-- name: InsertStagedAppVersion :execrows
INSERT INTO workos_core.app_versions (
    id, owner_user_id, app_id, version, scope, name, permissions,
    manifest_digest, canonical_manifest, state, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'staged', $10)
ON CONFLICT DO NOTHING;

-- name: InsertRepairCandidateVersion :execrows
INSERT INTO workos_core.app_repair_candidate_versions (
    task_id, owner_user_id, project_id, installation_id, incident_id, build_job_id,
    source_digest, app_version_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (task_id) DO NOTHING;

-- name: PublishStagedAppVersion :execrows
UPDATE workos_core.app_versions
SET state = 'published'
WHERE id = $1 AND state = 'staged';

-- name: MarkCandidateVersionPublished :execrows
UPDATE workos_core.app_repair_candidate_versions
SET published_at = $2
WHERE task_id = $1 AND published_at IS NULL;
