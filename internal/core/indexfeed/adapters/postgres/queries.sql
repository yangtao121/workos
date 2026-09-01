-- Core index publication feed facts (migration 026). Every statement here
-- touches only workos_core.index_publications; other tables are reached
-- through their owning modules.

-- name: AppendIndexPublication :execrows
INSERT INTO workos_core.index_publications (
    id, operation, owner_user_id, project_id, source_type, source_id,
    artifact_type, digest, occurred_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(operation), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    sqlc.arg(source_type), sqlc.arg(source_id), sqlc.arg(artifact_type),
    sqlc.arg(digest), sqlc.arg(occurred_at), sqlc.arg(created_at)
);

-- name: ClaimPendingIndexPublications :many
UPDATE workos_core.index_publications AS pub
SET claim_locked_by = sqlc.arg(worker_id),
    claim_token = sqlc.arg(claim_token),
    claim_locked_until = sqlc.arg(lease_until),
    claim_attempts = pub.claim_attempts + 1
WHERE pub.id IN (
    SELECT pending.id FROM workos_core.index_publications AS pending
    WHERE pending.outcome IS NULL
      AND (pending.claim_locked_until IS NULL OR pending.claim_locked_until < sqlc.arg(now))
    ORDER BY pending.occurred_at, pending.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(max_batch)
)
RETURNING pub.id, pub.operation, pub.owner_user_id, pub.project_id, pub.source_type,
          pub.source_id, pub.artifact_type, pub.digest, pub.occurred_at, pub.claim_locked_until;

-- name: LockIndexPublicationForResolve :one
SELECT id, operation, owner_user_id, project_id, source_type, source_id,
       artifact_type, digest, occurred_at
FROM workos_core.index_publications
WHERE id = sqlc.arg(id)
  AND claim_locked_by = sqlc.arg(worker_id)
  AND claim_token = sqlc.arg(claim_token)
  AND outcome IS NULL
  AND claim_locked_until IS NOT NULL
  AND claim_locked_until > sqlc.arg(now)
FOR UPDATE;

-- name: CompleteIndexPublication :execrows
UPDATE workos_core.index_publications
SET outcome = sqlc.arg(outcome),
    completed_at = sqlc.arg(now),
    completed_by = sqlc.arg(worker_id),
    claim_locked_by = NULL,
    claim_token = NULL,
    claim_locked_until = NULL
WHERE id = sqlc.arg(id)
  AND claim_locked_by = sqlc.arg(worker_id)
  AND claim_token = sqlc.arg(claim_token)
  AND outcome IS NULL
  AND claim_locked_until IS NOT NULL
  AND claim_locked_until > sqlc.arg(now);
