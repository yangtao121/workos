-- name: InsertNativeSession :execrows
INSERT INTO workos_runtime.native_sessions (
    session_id, owner_user_id, project_id, idempotency_key, request_digest,
    state, width, height, created_at, updated_at, expires_at, lifecycle_mode, app_kind
) VALUES ($1, $2, $3, $4, $5, 'queued', $6, $7, $8, $8, $9, $10, $11)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetNativeSession :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at, generation, lifecycle_mode,
       child_container_id, child_image_id, child_generation, app_kind
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND session_id = $2;

-- name: GetNativeSessionByKey :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at, generation, lifecycle_mode,
       child_container_id, child_image_id, child_generation, app_kind
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: UpdateNativeSessionState :execrows
UPDATE workos_runtime.native_sessions
SET state = $3, updated_at = $4
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running');

-- name: CloseNativeSession :execrows
UPDATE workos_runtime.native_sessions
SET state = $3, updated_at = $4, expires_at = CASE WHEN lifecycle_mode=1 THEN $4::timestamptz ELSE NULL END
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running');

-- name: CountActiveNativeSessions :one
SELECT count(*) AS active
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND state IN ('queued', 'running');

-- name: ExpireIdleNativeSessions :many
UPDATE workos_runtime.native_sessions
SET state = 'closed', updated_at = $1, expires_at = $1
WHERE state IN ('queued', 'running') AND lifecycle_mode=1 AND expires_at <= $1
RETURNING session_id::text AS session_id;

-- name: ListProjectNativeSessions :many
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at, generation, lifecycle_mode,
       child_container_id, child_image_id, child_generation, app_kind
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND project_id = $2 AND state IN ('queued', 'running');

-- name: ListActiveNativeSessions :many
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at, generation, lifecycle_mode,
       child_container_id, child_image_id, child_generation, app_kind
FROM workos_runtime.native_sessions
WHERE state IN ('queued', 'running');

-- name: LockNativeRestart :one
SELECT generation FROM workos_runtime.native_sessions WHERE owner_user_id=$1 AND session_id=$2 FOR UPDATE;

-- name: GetNativeRestartReceipt :one
SELECT generation, lifecycle_mode FROM workos_runtime.native_session_restarts WHERE session_id=$1 AND action_key=$2;

-- name: BeginNativeRestart :one
UPDATE workos_runtime.native_sessions SET generation=generation+1,state='queued',updated_at=$3,expires_at=$4,lifecycle_mode=$5
WHERE owner_user_id=$1 AND session_id=$2 RETURNING generation;

-- name: RecordNativeRestart :exec
INSERT INTO workos_runtime.native_session_restarts(session_id,action_key,generation,lifecycle_mode) VALUES($1,$2,$3,$4);

-- name: GetNativeStopReceipt :one
SELECT generation FROM workos_runtime.native_session_stops WHERE session_id=sqlc.arg(session_id)::uuid AND action_key=sqlc.arg(action_key);

-- name: RecordNativeStop :exec
INSERT INTO workos_runtime.native_session_stops(session_id,action_key,generation) VALUES(sqlc.arg(session_id)::uuid,sqlc.arg(action_key),sqlc.arg(generation));

-- name: BindNativeChild :execrows
UPDATE workos_runtime.native_sessions
SET child_container_id = sqlc.arg(child_container_id),
    child_image_id = sqlc.arg(child_image_id),
    child_generation = sqlc.arg(child_generation)
WHERE owner_user_id = sqlc.arg(owner_user_id)::uuid
  AND session_id = sqlc.arg(session_id)::uuid
  AND generation = sqlc.arg(child_generation)
  AND state IN ('queued', 'running')
  AND (child_generation IS NULL OR child_generation < sqlc.arg(child_generation)
       OR (child_container_id = sqlc.arg(child_container_id) AND child_image_id = sqlc.arg(child_image_id)));
