-- Indexer projection queries. Every statement touches only workos_index
-- tables; Core schemas are reached exclusively through the private RPC
-- client. Search reads only the active generation.

-- name: ActiveGenerationID :one
SELECT generation_id FROM workos_index.active_generation;

-- name: WritableGenerationIDs :many
SELECT id FROM workos_index.projection_generations
WHERE status IN ('building', 'active')
ORDER BY status = 'active' DESC, created_at, id;

-- name: InsertGeneration :exec
INSERT INTO workos_index.projection_generations (id, scope, owner_user_id, project_id, status, created_at)
VALUES (sqlc.arg(id), sqlc.arg(scope), sqlc.arg(owner_user_id), sqlc.arg(project_id), sqlc.arg(status), sqlc.arg(created_at));

-- name: PromoteGeneration :execrows
UPDATE workos_index.active_generation
SET generation_id = sqlc.arg(target)
WHERE generation_id = (SELECT generation_id FROM workos_index.active_generation FOR UPDATE)
  AND sqlc.arg(expect_current) IN (SELECT generation_id FROM workos_index.active_generation)
  AND generation_id = sqlc.arg(expect_current);

-- name: ActivateGenerationIfEmpty :execrows
INSERT INTO workos_index.active_generation (generation_id)
SELECT sqlc.arg(generation_id)
WHERE NOT EXISTS (SELECT 1 FROM workos_index.active_generation);

-- name: CountGenerationDocuments :one
SELECT count(*) FROM workos_index.documents WHERE projection_generation = sqlc.arg(generation_id);

-- name: GetBuildingGenerationForScope :one
SELECT id FROM workos_index.projection_generations
WHERE status = 'building' AND scope = sqlc.arg(scope)
  AND owner_user_id IS NOT DISTINCT FROM sqlc.arg(owner_user_id)
  AND project_id IS NOT DISTINCT FROM sqlc.arg(project_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: UpsertSearchDocument :execrows
INSERT INTO workos_index.documents (
    projection_generation, owner_user_id, project_id, source_type, source_id,
    source_digest, artifact_type, title, content, source_created_at,
    last_publication_id, source_operation, indexed_at, updated_at
) VALUES (
    sqlc.arg(projection_generation), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    'artifact.review.v1', sqlc.arg(source_id), sqlc.arg(source_digest),
    sqlc.arg(artifact_type), sqlc.arg(title), sqlc.arg(content),
    sqlc.arg(source_created_at), sqlc.arg(last_publication_id),
    'review-artifact.upsert', sqlc.arg(indexed_at), sqlc.arg(updated_at)
)
ON CONFLICT (projection_generation, owner_user_id, project_id, source_id) DO UPDATE
SET source_digest = EXCLUDED.source_digest,
    artifact_type = EXCLUDED.artifact_type,
    title = EXCLUDED.title,
    content = EXCLUDED.content,
    source_created_at = EXCLUDED.source_created_at,
    last_publication_id = EXCLUDED.last_publication_id,
    indexed_at = EXCLUDED.indexed_at,
    tombstoned_at = NULL,
    updated_at = EXCLUDED.updated_at
WHERE workos_index.documents.last_publication_id <= EXCLUDED.last_publication_id
   OR workos_index.documents.tombstoned_at IS NULL AND workos_index.documents.source_digest = EXCLUDED.source_digest;

-- name: TombstoneProjectDocuments :execrows
UPDATE workos_index.documents
SET tombstoned_at = sqlc.arg(tombstoned_at), updated_at = sqlc.arg(updated_at)
WHERE projection_generation = sqlc.arg(projection_generation)
  AND owner_user_id = sqlc.arg(owner_user_id) AND project_id = sqlc.arg(project_id)
  AND tombstoned_at IS NULL;

-- name: UpsertProjectTombstone :exec
INSERT INTO workos_index.project_tombstones (owner_user_id, project_id, last_publication_id, archived_at)
VALUES (sqlc.arg(owner_user_id), sqlc.arg(project_id), sqlc.arg(last_publication_id), sqlc.arg(archived_at))
ON CONFLICT (owner_user_id, project_id) DO UPDATE
SET last_publication_id = EXCLUDED.last_publication_id, archived_at = EXCLUDED.archived_at
WHERE workos_index.project_tombstones.last_publication_id <= EXCLUDED.last_publication_id;

-- name: GetProjectTombstone :one
SELECT owner_user_id, project_id, last_publication_id, archived_at
FROM workos_index.project_tombstones
WHERE owner_user_id = sqlc.arg(owner_user_id) AND project_id = sqlc.arg(project_id);

-- name: UpsertReceipt :exec
INSERT INTO workos_index.publication_receipts (
    publication_id, projection_generation, request_digest, outcome, source_digest, processed_at
) VALUES (
    sqlc.arg(publication_id), sqlc.arg(projection_generation), sqlc.arg(request_digest),
    sqlc.arg(outcome), sqlc.arg(source_digest), sqlc.arg(processed_at)
)
ON CONFLICT (publication_id, projection_generation) DO NOTHING;

-- name: GetReceipt :one
SELECT publication_id, projection_generation, request_digest, outcome, source_digest, processed_at
FROM workos_index.publication_receipts
WHERE publication_id = sqlc.arg(publication_id) AND projection_generation = sqlc.arg(projection_generation);

-- name: UpsertConsumerCursor :exec
INSERT INTO workos_index.consumer_state (worker_id, cursor_publication_id, cursor_occurred_at, observed_watermark, updated_at)
VALUES (sqlc.arg(worker_id), sqlc.arg(cursor_publication_id), sqlc.arg(cursor_occurred_at), sqlc.arg(observed_watermark), sqlc.arg(updated_at))
ON CONFLICT (worker_id) DO UPDATE
SET cursor_publication_id = EXCLUDED.cursor_publication_id,
    cursor_occurred_at = EXCLUDED.cursor_occurred_at,
    updated_at = EXCLUDED.updated_at;

-- name: SearchFreshness :one
SELECT
    COALESCE((SELECT max(indexed_at) FROM workos_index.documents
              WHERE projection_generation = (SELECT generation_id FROM workos_index.active_generation)), 'epoch'::timestamptz)::timestamptz
    AS last_indexed_at;

-- name: GetConsumerCursor :one
SELECT worker_id, cursor_publication_id, cursor_occurred_at, observed_watermark, updated_at
FROM workos_index.consumer_state
WHERE worker_id = sqlc.arg(worker_id);

-- Deterministic lexical page (ADR-0013 §5): rank over the built-in 'simple'
-- tsquery, title hits weighted 2x, fixed tie-break (score DESC,
-- source_created_at DESC, source_id ASC). The cursor triple uses sentinel
-- values for the first page (score 1e9). The snapshot watermark excludes
-- documents indexed after the chain started, so late arrivals never join an
-- open page chain.
-- name: SearchProjectDocuments :many
WITH q AS (
    SELECT websearch_to_tsquery('simple', sqlc.arg(query_text)) AS tsq
),
scored AS (
    SELECT d.source_id, d.source_digest, d.artifact_type, d.title, d.source_created_at, d.content,
           d.last_publication_id, d.indexed_at,
           ((CASE WHEN d.title_tsv @@ q.tsq THEN ts_rank(d.title_tsv, q.tsq) ELSE 0.0::double precision END) * 2.0
           + (CASE WHEN d.body_tsv @@ q.tsq THEN ts_rank(d.body_tsv, q.tsq) ELSE 0.0::double precision END))::double precision AS score
    FROM workos_index.documents d, q
    WHERE d.projection_generation = sqlc.arg(generation_id)
      AND d.owner_user_id = sqlc.arg(owner_user_id)
      AND d.project_id = sqlc.arg(project_id)
      AND d.tombstoned_at IS NULL
      AND d.indexed_at <= sqlc.arg(snapshot_through)
      AND (d.title_tsv @@ q.tsq OR d.body_tsv @@ q.tsq)
)
SELECT source_id, source_digest, artifact_type, title, source_created_at, content,
       last_publication_id, indexed_at, score
FROM scored
WHERE score > 0
  AND (score < sqlc.arg(cursor_score)::double precision
       OR (score = sqlc.arg(cursor_score)::double precision
           AND (source_created_at < sqlc.arg(cursor_created_at)::timestamptz
                OR (source_created_at = sqlc.arg(cursor_created_at)::timestamptz
                    AND source_id > sqlc.arg(cursor_source_id)::uuid))))
ORDER BY score DESC, source_created_at DESC, source_id
LIMIT sqlc.arg(row_limit);

-- Repair/reindex jobs (IndexContext). The request mapping is the durable
-- idempotency authority; job + sources + first-response snapshot commit in
-- one transaction.

-- name: GetIndexJobRequest :one
SELECT owner_user_id, idempotency_key, request_digest, result, created_at
FROM workos_index.index_job_requests
WHERE owner_user_id = sqlc.arg(owner_user_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertIndexJob :one
INSERT INTO workos_index.index_jobs (id, owner_user_id, project_id, state, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(project_id), 'pending', sqlc.arg(created_at), sqlc.arg(updated_at))
RETURNING id;

-- name: InsertIndexJobSource :exec
INSERT INTO workos_index.index_job_sources (job_id, artifact_id, expected_digest, state, updated_at)
VALUES (sqlc.arg(job_id), sqlc.arg(artifact_id), sqlc.arg(expected_digest), 'pending', sqlc.arg(updated_at));

-- name: InsertIndexJobRequest :exec
INSERT INTO workos_index.index_job_requests (owner_user_id, idempotency_key, request_digest, result, created_at)
VALUES (sqlc.arg(owner_user_id), sqlc.arg(idempotency_key), sqlc.arg(request_digest), sqlc.arg(result), sqlc.arg(created_at));

-- name: UpdateIndexJobState :exec
UPDATE workos_index.index_jobs SET state = sqlc.arg(state), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: ClaimRunnableIndexJob :one
UPDATE workos_index.index_jobs SET state = 'running', updated_at = sqlc.arg(updated_at)
WHERE id IN (
    SELECT j.id FROM workos_index.index_jobs j
    WHERE j.state IN ('pending', 'running')
    ORDER BY j.created_at, j.id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, owner_user_id, project_id, state, failure_category, created_at, updated_at;

-- name: ListIndexJobSources :many
SELECT job_id, artifact_id, expected_digest, state, outcome, updated_at
FROM workos_index.index_job_sources
WHERE job_id = sqlc.arg(job_id)
ORDER BY artifact_id;

-- name: UpdateIndexJobSource :exec
UPDATE workos_index.index_job_sources SET state = sqlc.arg(state), outcome = sqlc.arg(outcome), updated_at = sqlc.arg(updated_at)
WHERE job_id = sqlc.arg(job_id) AND artifact_id = sqlc.arg(artifact_id);

-- name: CountIndexJobSources :one
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE state = 'completed') AS completed,
    count(*) FILTER (WHERE state = 'failed') AS failed
FROM workos_index.index_job_sources WHERE job_id = sqlc.arg(job_id);

-- name: GetIndexJob :one
SELECT id, owner_user_id, project_id, state, failure_category, created_at, updated_at
FROM workos_index.index_jobs
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id);

-- name: GetIndexJobSources :many
SELECT job_id, artifact_id, expected_digest, state, outcome, updated_at
FROM workos_index.index_job_sources
WHERE job_id = sqlc.arg(job_id)
ORDER BY artifact_id;

-- name: MarkIndexJobFailed :exec
UPDATE workos_index.index_jobs SET state = 'failed', failure_category = sqlc.arg(failure_category), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND state = 'running';

-- name: GetDocumentStatus :one
SELECT source_digest, tombstoned_at
FROM workos_index.documents
WHERE projection_generation = (SELECT generation_id FROM workos_index.active_generation)
  AND owner_user_id = sqlc.arg(owner_user_id) AND project_id = sqlc.arg(project_id)
  AND source_id = sqlc.arg(source_id);

-- Shadow-generation rebuild facts (ADR-0013 §9). Generations and rebuild
-- jobs are durable: a restart resumes from the stored phase and cursor.

-- name: InsertGenerationFull :exec
INSERT INTO workos_index.projection_generations (
    id, scope, owner_user_id, project_id, status, snapshot_boundary, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(scope), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    sqlc.arg(status), sqlc.arg(snapshot_boundary), sqlc.arg(created_at)
);

-- name: UpdateGenerationStatus :exec
UPDATE workos_index.projection_generations
SET status = sqlc.arg(status), promoted_at = sqlc.arg(promoted_at), retired_at = sqlc.arg(retired_at)
WHERE id = sqlc.arg(id);

-- name: GetGeneration :one
SELECT id, scope, owner_user_id, project_id, status, snapshot_boundary,
       document_count, tombstone_count, created_at, promoted_at, retired_at
FROM workos_index.projection_generations WHERE id = sqlc.arg(id);

-- name: CountGenerationDocs :one
SELECT
    count(*) FILTER (WHERE tombstoned_at IS NULL) AS documents,
    count(*) FILTER (WHERE tombstoned_at IS NOT NULL) AS tombstoned
FROM workos_index.documents WHERE projection_generation = sqlc.arg(generation_id);

-- Promote is a single-row compare-and-swap: the winner held the previous
-- active generation at commit time, so a stale or failed worker can never
-- overwrite a later successful promotion.
-- name: CasPromoteGeneration :execrows
UPDATE workos_index.active_generation
SET generation_id = sqlc.arg(target)
WHERE generation_id = sqlc.arg(expect_current);

-- name: InsertRebuildJob :exec
INSERT INTO workos_index.rebuild_jobs (
    id, scope, owner_user_id, project_id, idempotency_digest, state,
    target_generation, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(scope), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    sqlc.arg(idempotency_digest), 'requested', sqlc.arg(target_generation),
    sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: InsertRebuildJobRequest :exec
INSERT INTO workos_index.rebuild_job_requests (idempotency_key, request_digest, job_id, created_at)
VALUES (sqlc.arg(idempotency_key), sqlc.arg(request_digest), sqlc.arg(job_id), sqlc.arg(created_at));

-- name: GetRebuildJobRequest :one
SELECT idempotency_key, request_digest, job_id, created_at
FROM workos_index.rebuild_job_requests WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetRebuildJob :one
SELECT id, scope, owner_user_id, project_id, idempotency_digest, state,
       target_generation, phase_cursor, snapshot_boundary, source_count,
       applied_count, tombstone_count, failure_category, created_at,
       updated_at, terminal_at
FROM workos_index.rebuild_jobs WHERE id = sqlc.arg(id);

-- name: GetLiveRebuildJobs :many
SELECT id, scope, owner_user_id, project_id, idempotency_digest, state,
       target_generation, phase_cursor, snapshot_boundary, source_count,
       applied_count, tombstone_count, failure_category, created_at,
       updated_at, terminal_at
FROM workos_index.rebuild_jobs
WHERE state IN ('requested', 'snapshotting', 'catching_up', 'validating', 'promoting')
ORDER BY created_at, id;

-- name: UpdateRebuildJob :exec
UPDATE workos_index.rebuild_jobs
SET state = sqlc.arg(state), phase_cursor = sqlc.arg(phase_cursor),
    snapshot_boundary = sqlc.arg(snapshot_boundary), source_count = sqlc.arg(source_count),
    applied_count = sqlc.arg(applied_count), tombstone_count = sqlc.arg(tombstone_count),
    failure_category = sqlc.arg(failure_category), updated_at = sqlc.arg(updated_at),
    terminal_at = sqlc.arg(terminal_at)
WHERE id = sqlc.arg(id);

-- name: CancelRebuildJob :execrows
UPDATE workos_index.rebuild_jobs
SET state = 'canceled', failure_category = sqlc.arg(failure_category),
    updated_at = sqlc.arg(updated_at), terminal_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND state IN ('requested', 'snapshotting', 'catching_up', 'validating');

-- name: ApplyResolvedSourceToGeneration :execrows
INSERT INTO workos_index.documents (
    projection_generation, owner_user_id, project_id, source_type, source_id,
    source_digest, artifact_type, title, content, source_created_at,
    last_publication_id, source_operation, indexed_at, updated_at
) VALUES (
    sqlc.arg(projection_generation), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    'artifact.review.v1', sqlc.arg(source_id), sqlc.arg(source_digest),
    sqlc.arg(artifact_type), sqlc.arg(title), sqlc.arg(content),
    sqlc.arg(source_created_at), sqlc.arg(last_publication_id),
    'review-artifact.upsert', sqlc.arg(indexed_at), sqlc.arg(updated_at)
)
ON CONFLICT (projection_generation, owner_user_id, project_id, source_id) DO UPDATE
SET source_digest = EXCLUDED.source_digest,
    artifact_type = EXCLUDED.artifact_type,
    title = EXCLUDED.title,
    content = EXCLUDED.content,
    source_created_at = EXCLUDED.source_created_at,
    last_publication_id = EXCLUDED.last_publication_id,
    indexed_at = EXCLUDED.indexed_at,
    tombstoned_at = NULL,
    updated_at = EXCLUDED.updated_at;

-- name: TombstoneGenerationDocuments :execrows
UPDATE workos_index.documents
SET tombstoned_at = sqlc.arg(tombstoned_at), updated_at = sqlc.arg(updated_at)
WHERE projection_generation = sqlc.arg(projection_generation)
  AND owner_user_id = sqlc.arg(owner_user_id) AND project_id = sqlc.arg(project_id)
  AND tombstoned_at IS NULL;

-- name: UpsertReceiptForGeneration :exec
INSERT INTO workos_index.publication_receipts (
    publication_id, projection_generation, request_digest, outcome, source_digest, processed_at
) VALUES (
    sqlc.arg(publication_id), sqlc.arg(projection_generation), sqlc.arg(request_digest),
    sqlc.arg(outcome), sqlc.arg(source_digest), sqlc.arg(processed_at)
)
ON CONFLICT (publication_id, projection_generation) DO NOTHING;

-- name: WalkGenerationDocuments :many
SELECT source_id, source_digest, artifact_type, source_created_at, tombstoned_at
FROM workos_index.documents
WHERE projection_generation = sqlc.arg(generation_id)
ORDER BY source_created_at, source_id
LIMIT sqlc.arg(page_limit);

-- name: WalkGenerationDocumentsAfter :many
SELECT source_id, source_digest, artifact_type, source_created_at, tombstoned_at
FROM workos_index.documents
WHERE projection_generation = sqlc.arg(generation_id)
  AND (source_created_at, source_id) > (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_source_id)::uuid)
ORDER BY source_created_at, source_id
LIMIT sqlc.arg(page_limit);
