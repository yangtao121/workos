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

-- Metadata projection shared by both implemented subtypes. Exactly one branch
-- matches a given (owner, id); the union keeps the read a single snapshot.
-- Provenance columns are NULL on the side that does not carry them.
-- name: GetArtifactMetadataUnion :one
SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
       file_count, total_size_bytes, created_at, entrypoint, project_id, source_task_id,
       output_key, line_count, review_content
FROM (
    SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
           file_count, total_size_bytes, created_at, entrypoint,
           NULL::uuid AS project_id, NULL::uuid AS source_task_id,
           NULL::text AS output_key, NULL::integer AS line_count,
           NULL::bytea AS review_content
    FROM workos_core.web_bundle_artifacts w
    WHERE w.owner_user_id = sqlc.arg(owner_user_id) AND w.id = sqlc.arg(artifact_id)
    UNION ALL
    SELECT id, owner_user_id, type, title, media_type, ''::text AS content_ref, digest,
           1 AS file_count, byte_count AS total_size_bytes, created_at,
           ''::text AS entrypoint, project_id, source_task_id,
           output_key, line_count, content AS review_content
    FROM workos_core.project_review_artifacts p
    WHERE p.owner_user_id = sqlc.arg(owner_user_id) AND p.id = sqlc.arg(artifact_id)
) AS artifact;

-- Ordered union page across both subtypes, probing one row beyond the limit.
-- name: ListArtifactIDPageUnion :many
SELECT id FROM (
    SELECT w.id FROM workos_core.web_bundle_artifacts w
    WHERE w.owner_user_id = sqlc.arg(owner_user_id)
      AND (sqlc.arg(cursor) = '' OR w.id > sqlc.arg(cursor)::uuid)
    UNION ALL
    SELECT p.id FROM workos_core.project_review_artifacts p
    WHERE p.owner_user_id = sqlc.arg(owner_user_id)
      AND (sqlc.arg(cursor) = '' OR p.id > sqlc.arg(cursor)::uuid)
) AS ids
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- Summary projection shared by both subtypes for exactly the given IDs.
-- name: ListArtifactSummariesUnion :many
SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
       file_count, total_size_bytes, created_at, entrypoint, project_id, source_task_id,
       output_key, line_count, review_content
FROM (
    SELECT id, owner_user_id, type, title, media_type, content_ref, digest,
           file_count, total_size_bytes, created_at, entrypoint,
           NULL::uuid AS project_id, NULL::uuid AS source_task_id,
           NULL::text AS output_key, NULL::integer AS line_count,
           NULL::bytea AS review_content
    FROM workos_core.web_bundle_artifacts w
    WHERE w.owner_user_id = sqlc.arg(owner_user_id) AND w.id = ANY(sqlc.arg(ids)::uuid[])
    UNION ALL
    SELECT id, owner_user_id, type, title, media_type, ''::text AS content_ref, digest,
           1 AS file_count, byte_count AS total_size_bytes, created_at,
           ''::text AS entrypoint, project_id, source_task_id,
           output_key, line_count, content AS review_content
    FROM workos_core.project_review_artifacts p
    WHERE p.owner_user_id = sqlc.arg(owner_user_id) AND p.id = ANY(sqlc.arg(ids)::uuid[])
) AS artifact
ORDER BY id;

-- One review artifact's authoritative metadata and exact content bytes from
-- the same row snapshot.
-- name: GetReviewArtifactContent :one
SELECT id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
       output_key, byte_count, line_count, content, created_at
FROM workos_core.project_review_artifacts
WHERE owner_user_id = sqlc.arg(owner_user_id) AND id = sqlc.arg(artifact_id);

-- name: ListProjectReviewArtifactIDPage :many
SELECT id
FROM workos_core.project_review_artifacts
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND project_id = sqlc.arg(project_id)::uuid
  AND (sqlc.arg(cursor) = '' OR id > sqlc.arg(cursor)::uuid)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- Adjudication mapping read for replay/conflict classification inside the
-- materialization coordinator's transaction.
-- name: GetReviewArtifactOutput :one
SELECT task_id, output_key, artifact_type, request_digest, owner_user_id, project_id,
       artifact_id, event_id, event_sequence, event_occurred_at, created_at
FROM workos_core.project_review_artifact_outputs
WHERE task_id = sqlc.arg(task_id)::uuid AND output_key = sqlc.arg(output_key);

-- name: InsertReviewArtifact :exec
INSERT INTO workos_core.project_review_artifacts (
    id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
    output_key, byte_count, line_count, content, created_at
) VALUES (
    sqlc.arg('id'), sqlc.arg('owner_user_id'), sqlc.arg('type'), sqlc.arg('title'),
    sqlc.arg('media_type'), sqlc.arg('digest'), sqlc.arg('project_id'),
    sqlc.arg('source_task_id'), sqlc.arg('output_key'), sqlc.arg('byte_count'),
    sqlc.arg('line_count'), sqlc.arg('content'), sqlc.arg('created_at')
);

-- The adjudication insert is the physical arbiter: ON CONFLICT DO NOTHING
-- covers both the (task, output key) primary key and the (task, artifact
-- type) unique index, so a racing loser observes zero rows and re-classifies
-- with GetReviewArtifactOutput.
-- name: InsertReviewArtifactOutput :execrows
INSERT INTO workos_core.project_review_artifact_outputs (
    task_id, output_key, artifact_type, request_digest, owner_user_id, project_id,
    artifact_id, event_id, event_sequence, event_occurred_at, created_at
) VALUES (
    sqlc.arg('task_id'), sqlc.arg('output_key'), sqlc.arg('artifact_type'),
    sqlc.arg('request_digest'), sqlc.arg('owner_user_id'), sqlc.arg('project_id'),
    sqlc.arg('artifact_id'), sqlc.arg('event_id'), sqlc.arg('event_sequence'),
    sqlc.arg('event_occurred_at'), sqlc.arg('created_at')
)
ON CONFLICT DO NOTHING;

-- Replay read of one stored review artifact row (identity re-validated by
-- the caller against the lease-derived owner/project/task).
-- name: GetReviewFact :one
SELECT id, owner_user_id, type, title, media_type, digest, project_id, source_task_id,
       output_key, byte_count, line_count, content, created_at
FROM workos_core.project_review_artifacts
WHERE id = sqlc.arg(artifact_id)::uuid;
