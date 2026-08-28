-- name: InsertSession :exec
INSERT INTO workos_runtime.surface_sessions (
    id, owner_user_id, device_id, idempotency_key, request_digest,
    project_id, app_instance_id, renderer, app_id, app_version,
    manifest_digest, artifact_id, artifact_digest, entrypoint, path,
    bridge_token_hash, bridge_capabilities,
    created_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19);

-- name: GetSessionRequest :one
SELECT owner_user_id, idempotency_key, request_digest, session_id, created_at
FROM workos_runtime.surface_session_requests
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: InsertSessionRequest :execrows
INSERT INTO workos_runtime.surface_session_requests (
    owner_user_id, idempotency_key, request_digest, session_id, created_at
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING;

-- name: GetSession :one
SELECT id, owner_user_id, device_id, idempotency_key, request_digest,
       project_id, app_instance_id, renderer, app_id, app_version,
       manifest_digest, artifact_id, artifact_digest, entrypoint, path,
       bridge_token_hash, bridge_capabilities,
       created_at, expires_at, closed_at
FROM workos_runtime.surface_sessions
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3;

-- name: GetActiveSession :one
SELECT id, owner_user_id, device_id, idempotency_key, request_digest,
       project_id, app_instance_id, renderer, app_id, app_version,
       manifest_digest, artifact_id, artifact_digest, entrypoint, path,
       bridge_token_hash, bridge_capabilities,
       created_at, expires_at, closed_at
FROM workos_runtime.surface_sessions
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3
  AND closed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: RotateSessionBridgeToken :execrows
UPDATE workos_runtime.surface_sessions
SET bridge_token_hash = sqlc.arg(token_hash)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND device_id = sqlc.arg(device_id)
  AND id = sqlc.arg(session_id)
  AND closed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: GetActiveSessionByBridgeToken :one
SELECT id, owner_user_id, device_id, idempotency_key, request_digest,
       project_id, app_instance_id, renderer, app_id, app_version,
       manifest_digest, artifact_id, artifact_digest, entrypoint, path,
       bridge_token_hash, bridge_capabilities,
       created_at, expires_at, closed_at
FROM workos_runtime.surface_sessions
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND bridge_token_hash = sqlc.arg(token_hash)
  AND closed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: ClearSessionBridgeToken :execrows
UPDATE workos_runtime.surface_sessions
SET bridge_token_hash = NULL
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3 AND closed_at IS NULL;

-- name: CloseSession :execrows
UPDATE workos_runtime.surface_sessions
SET closed_at = sqlc.arg(now)
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3 AND closed_at IS NULL;
