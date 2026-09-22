-- name: InsertSession :exec
-- installation_grant_revision is the create-time grant epoch Core's private
-- resolver returned for this session; the application must always pass the
-- resolved value, never a constant, so a session created after a SetAppGrants
-- mutation pins the epoch the user re-opened under.
INSERT INTO workos_runtime.surface_sessions (
    id, owner_user_id, device_id, idempotency_key, request_digest,
    project_id, app_instance_id, renderer, app_id, app_version,
    manifest_digest, artifact_id, artifact_digest, entrypoint, path,
    workload_id, workload_generation,
    bridge_token_hash, bridge_capabilities, installation_grant_revision,
    created_at, expires_at, lifecycle_mode
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23);

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
       workload_id, workload_generation,
       bridge_token_hash, bridge_capabilities, installation_grant_revision,
       created_at, expires_at, closed_at, lifecycle_mode
FROM workos_runtime.surface_sessions
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3;

-- name: GetActiveSession :one
SELECT id, owner_user_id, device_id, idempotency_key, request_digest,
       project_id, app_instance_id, renderer, app_id, app_version,
       manifest_digest, artifact_id, artifact_digest, entrypoint, path,
       workload_id, workload_generation,
       bridge_token_hash, bridge_capabilities, installation_grant_revision,
       created_at, expires_at, closed_at, lifecycle_mode
FROM workos_runtime.surface_sessions
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3
  AND closed_at IS NULL AND expires_at > sqlc.arg(now);

-- name: CloseSession :execrows
UPDATE workos_runtime.surface_sessions
SET closed_at = sqlc.arg(now),
    bridge_token_hash = NULL
WHERE owner_user_id = $1 AND device_id = $2 AND id = $3 AND closed_at IS NULL;

-- name: RotateSessionBridgeToken :one
UPDATE workos_runtime.surface_sessions
SET bridge_token_hash = sqlc.arg(token_hash)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND device_id = sqlc.arg(device_id)
  AND id = sqlc.arg(session_id)
  AND closed_at IS NULL
  AND expires_at > sqlc.arg(now)
RETURNING id, owner_user_id, device_id, idempotency_key, request_digest,
          project_id, app_instance_id, renderer, app_id, app_version,
          manifest_digest, artifact_id, artifact_digest, entrypoint, path,
          workload_id, workload_generation,
          bridge_token_hash, bridge_capabilities, installation_grant_revision,
          created_at, expires_at, closed_at, lifecycle_mode;

-- name: GetActiveSessionByBridgeToken :one
SELECT id, owner_user_id, device_id, idempotency_key, request_digest,
       project_id, app_instance_id, renderer, app_id, app_version,
       manifest_digest, artifact_id, artifact_digest, entrypoint, path,
       workload_id, workload_generation,
       bridge_token_hash, bridge_capabilities, installation_grant_revision,
       created_at, expires_at, closed_at, lifecycle_mode
FROM workos_runtime.surface_sessions
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND bridge_token_hash = sqlc.arg(token_hash)
  AND closed_at IS NULL
  AND expires_at > sqlc.arg(now);

-- name: HasActiveSessionForInstance :one
-- The idle-TTL source for the Workload Manager: whether any open, unexpired
-- session still references the installed instance.
SELECT EXISTS (
    SELECT 1 FROM workos_runtime.surface_sessions
    WHERE owner_user_id = sqlc.arg(owner_user_id)
      AND app_instance_id = sqlc.arg(app_instance_id)
      AND closed_at IS NULL
      AND expires_at > sqlc.arg(now)
) AS has_active;

-- Surface continuity facts (ADR-0031, migration 059): attachments are
-- per-device access relations to supervised interactive workloads; control is
-- a single-controller epoch advanced only by the explicit takeover RPC.

-- name: InsertSurfaceAttachment :execrows
INSERT INTO workos_runtime.surface_attachments (
    attachment_id, workload_id, surface_session_id, owner_user_id, project_id,
    device_id, idempotency_key, controls, control_generation, state,
    attached_at, control_expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (owner_user_id, idempotency_key) DO NOTHING;

-- name: GetSurfaceAttachmentByKey :one
SELECT attachment_id, workload_id, surface_session_id, owner_user_id, project_id,
       device_id, idempotency_key, controls, control_generation, state,
       attached_at, control_expires_at, detached_at
FROM workos_runtime.surface_attachments
WHERE owner_user_id = $1 AND idempotency_key = $2;

-- name: GetSurfaceAttachment :one
SELECT attachment_id, workload_id, surface_session_id, owner_user_id, project_id,
       device_id, idempotency_key, controls, control_generation, state,
       attached_at, control_expires_at, detached_at
FROM workos_runtime.surface_attachments
WHERE owner_user_id = $1 AND attachment_id = $2;

-- name: GetControllerAttachment :one
SELECT attachment_id, workload_id, surface_session_id, owner_user_id, project_id,
       device_id, idempotency_key, controls, control_generation, state,
       attached_at, control_expires_at, detached_at
FROM workos_runtime.surface_attachments
WHERE owner_user_id = $1 AND attachment_id = $2 AND device_id = $3;

-- name: GetLiveAttachmentBySurfaceSession :one
SELECT attachment_id, workload_id, surface_session_id, owner_user_id, project_id,
       device_id, idempotency_key, controls, control_generation, state,
       attached_at, control_expires_at, detached_at
FROM workos_runtime.surface_attachments
WHERE owner_user_id = $1 AND surface_session_id = $2 AND device_id = $3
  AND state = 'attached'
ORDER BY attached_at DESC
LIMIT 1;

-- name: DetachSurfaceAttachment :execrows
UPDATE workos_runtime.surface_attachments
SET state = 'detached', controls = false, detached_at = sqlc.arg(now)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND attachment_id = sqlc.arg(attachment_id)
  AND state = 'attached';

-- name: MarkSurfaceAttachmentControl :execrows
UPDATE workos_runtime.surface_attachments
SET controls = sqlc.arg(controls),
    control_generation = sqlc.arg(control_generation),
    control_expires_at = sqlc.arg(control_expires_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND attachment_id = sqlc.arg(attachment_id);

-- name: ClearSurfaceAttachmentControl :execrows
UPDATE workos_runtime.surface_attachments
SET controls = false
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND attachment_id = sqlc.arg(attachment_id)
  AND control_generation <= sqlc.arg(control_generation);

-- name: GetSurfaceControlLease :one
SELECT workload_id, owner_user_id, control_generation, controller_attachment_id,
       controller_device_id, granted_at, expires_at
FROM workos_runtime.surface_control_leases
WHERE workload_id = $1;

-- name: LockSurfaceControlLease :one
SELECT workload_id, owner_user_id, control_generation, controller_attachment_id,
       controller_device_id, granted_at, expires_at
FROM workos_runtime.surface_control_leases
WHERE workload_id = $1
FOR UPDATE;

-- name: InsertSurfaceControlLease :exec
INSERT INTO workos_runtime.surface_control_leases (
    workload_id, owner_user_id, control_generation, controller_attachment_id,
    controller_device_id, granted_at, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpdateSurfaceControlLease :exec
UPDATE workos_runtime.surface_control_leases
SET control_generation = $2,
    controller_attachment_id = $3,
    controller_device_id = $4,
    granted_at = $5,
    expires_at = $6
WHERE workload_id = $1;

-- name: CountLiveSurfaceAttachments :many
SELECT workload_id::text AS workload_id, count(*)::int AS live_attachments
FROM workos_runtime.surface_attachments
WHERE owner_user_id = $1 AND project_id = $2 AND state = 'attached'
GROUP BY workload_id;

-- name: ListLiveSurfaceAttachmentWorkloads :many
SELECT DISTINCT workload_id::text AS workload_id, owner_user_id::text AS owner_user_id
FROM workos_runtime.surface_attachments
WHERE state = 'attached';

-- name: ExpireElapsedSurfaceAttachments :many
UPDATE workos_runtime.surface_attachments
SET state = 'expired', controls = false, detached_at = sqlc.arg(now)
WHERE state = 'attached'
  AND control_expires_at IS NOT NULL
  AND control_expires_at < sqlc.arg(now)
RETURNING attachment_id::text AS attachment_id;

-- name: ExpireSurfaceAttachmentsForWorkloads :execrows
UPDATE workos_runtime.surface_attachments
SET state = 'expired', controls = false, detached_at = sqlc.arg(now)
WHERE state = 'attached'
  AND workload_id = ANY(sqlc.arg(workload_ids)::uuid[]);

-- name: CountAppSurfaceDevices :one
SELECT count(DISTINCT device_id)::integer AS devices
FROM workos_runtime.surface_sessions
WHERE owner_user_id=sqlc.arg(owner_user_id) AND project_id=sqlc.arg(project_id)
  AND workload_id=sqlc.arg(workload_id)::uuid AND workload_generation=sqlc.arg(generation)
  AND renderer='web-service' AND closed_at IS NULL AND expires_at>sqlc.arg(now);
