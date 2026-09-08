-- name: InsertBrowserSession :execrows
INSERT INTO workos_runtime.browser_sessions (
    session_id, owner_user_id, project_id, idempotency_key, request_digest,
    state, current_url, restart_count, created_at, updated_at, expires_at
) VALUES ($1, $2, $3, $4, $5, 'queued', $6, 0, $7, $7, $8)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetBrowserSession :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, current_url, restart_count, created_at, updated_at, expires_at
FROM workos_runtime.browser_sessions
WHERE owner_user_id = $1 AND session_id = $2;

-- name: GetBrowserSessionByKey :one
SELECT session_id, owner_user_id, project_id, idempotency_key, request_digest,
       state, current_url, restart_count, created_at, updated_at, expires_at
FROM workos_runtime.browser_sessions
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: UpdateBrowserSessionRunning :execrows
UPDATE workos_runtime.browser_sessions
SET state = $3, current_url = $4, restart_count = $5, updated_at = $6
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running', 'restarting');

-- name: CloseBrowserSession :execrows
UPDATE workos_runtime.browser_sessions
SET state = $3, restart_count = $4, updated_at = $5, expires_at = $5
WHERE owner_user_id = $1 AND session_id = $2 AND state IN ('queued', 'running', 'restarting');

-- name: CountActiveBrowserSessions :one
SELECT count(*) AS active
FROM workos_runtime.browser_sessions
WHERE owner_user_id = $1 AND state IN ('queued', 'running', 'restarting');

-- name: ExpireIdleBrowserSessions :many
UPDATE workos_runtime.browser_sessions
SET state = 'closed', updated_at = $1, expires_at = $1
WHERE state IN ('queued', 'running', 'restarting') AND expires_at < $1
RETURNING session_id::text AS session_id;
