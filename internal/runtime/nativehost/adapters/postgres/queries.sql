-- name: InsertNativeSession :execrows
INSERT INTO workos_runtime.native_sessions (
    session_id, owner_user_id, project_id, idempotency_key, request_digest,
    state, width, height, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, 'queued', $6, $7, $8, $8, $9)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetNativeSession :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND session_id = $2;

-- name: GetNativeSessionByKey :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: UpdateNativeSessionState :execrows
UPDATE workos_runtime.native_sessions
SET state = $3, updated_at = $4
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running');

-- name: CloseNativeSession :execrows
UPDATE workos_runtime.native_sessions
SET state = $3, updated_at = $4, expires_at = $4
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running');

-- name: CountActiveNativeSessions :one
SELECT count(*) AS active
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND state IN ('queued', 'running');

-- name: ExpireIdleNativeSessions :many
UPDATE workos_runtime.native_sessions
SET state = 'closed', updated_at = $1, expires_at = $1
WHERE state IN ('queued', 'running') AND expires_at < $1
RETURNING session_id::text AS session_id;

-- name: ListProjectNativeSessions :many
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at
FROM workos_runtime.native_sessions
WHERE owner_user_id = $1 AND project_id = $2 AND state IN ('queued', 'running');

-- name: ListActiveNativeSessions :many
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, width, height, created_at, updated_at, expires_at
FROM workos_runtime.native_sessions
WHERE state IN ('queued', 'running');
