-- Core notification facts (ADR-0014, migration 029). Every statement here
-- touches only the workos_core notification tables; other modules are
-- reached through their owning modules' ports.

-- name: AllocateNotificationChangeSequence :one
INSERT INTO workos_core.notification_owner_sequences (
    owner_user_id, last_sequence, swept_through, updated_at
) VALUES (
    sqlc.arg(owner_user_id), 1, 0, sqlc.arg(updated_at)
)
ON CONFLICT (owner_user_id) DO UPDATE
SET last_sequence = workos_core.notification_owner_sequences.last_sequence + 1,
    updated_at = EXCLUDED.updated_at
RETURNING last_sequence;

-- name: SerializeNotificationRequest :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: InsertNotification :execrows
INSERT INTO workos_core.notifications (
    id, owner_user_id, project_id, kind, severity, origin, title, body,
    target_kind, target_id, app_id, app_installation_id,
    source_process, source_id, source_digest, created_at, created_change_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(project_id), sqlc.arg(kind),
    sqlc.arg(severity), sqlc.arg(origin), sqlc.arg(title), sqlc.arg(body),
    sqlc.arg(target_kind), sqlc.arg(target_id), sqlc.arg(app_id),
    sqlc.arg(app_installation_id), sqlc.arg(source_process), sqlc.arg(source_id),
    sqlc.arg(source_digest), sqlc.arg(created_at), sqlc.arg(created_change_sequence)
);

-- name: InsertNotificationChange :execrows
INSERT INTO workos_core.notification_changes (
    owner_user_id, change_sequence, notification_id, change_type, revision, occurred_at
) VALUES (
    sqlc.arg(owner_user_id), sqlc.arg(change_sequence), sqlc.arg(notification_id),
    sqlc.arg(change_type), sqlc.arg(revision), sqlc.arg(occurred_at)
);

-- name: InsertNotificationSourceReceipt :execrows
INSERT INTO workos_core.notification_source_receipts (
    source_process, source_id, source_digest, notification_id, recorded_at
) VALUES (
    sqlc.arg(source_process), sqlc.arg(source_id), sqlc.arg(source_digest),
    sqlc.arg(notification_id), sqlc.arg(recorded_at)
);

-- name: GetNotificationSourceReceipt :one
SELECT source_digest, notification_id FROM workos_core.notification_source_receipts
WHERE source_process = sqlc.arg(source_process) AND source_id = sqlc.arg(source_id);

-- name: GetNotificationByID :one
SELECT id, owner_user_id, project_id, kind, severity, origin, title, body,
       target_kind, target_id, app_id, app_installation_id, source_process,
       source_id, source_digest, created_at, read_at, read_change_sequence, created_change_sequence
FROM workos_core.notifications
WHERE id = sqlc.arg(id);

-- name: GetOwnerNotification :one
SELECT id, owner_user_id, project_id, kind, severity, origin, title, body,
       target_kind, target_id, app_id, app_installation_id, source_process,
       source_id, source_digest, created_at, read_at, read_change_sequence, created_change_sequence
FROM workos_core.notifications
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id);

-- name: LockOwnerNotifications :many
SELECT id, owner_user_id, project_id, kind, severity, origin, title, body,
       target_kind, target_id, app_id, app_installation_id, source_process,
       source_id, source_digest, created_at, read_at, read_change_sequence, created_change_sequence
FROM workos_core.notifications
WHERE owner_user_id = sqlc.arg(owner_user_id) AND id = ANY (sqlc.arg(ids)::uuid[])
ORDER BY created_at DESC, id DESC
FOR UPDATE;

-- name: MarkNotificationRead :execrows
UPDATE workos_core.notifications
SET read_at = sqlc.arg(read_at), read_change_sequence = sqlc.arg(read_change_sequence)
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id) AND read_at IS NULL;

-- name: CountOwnerUnread :one
SELECT count(*) FROM workos_core.notifications
WHERE owner_user_id = sqlc.arg(owner_user_id) AND read_at IS NULL;

-- name: GetOwnerChangeWatermark :one
SELECT COALESCE((
    SELECT last_sequence
    FROM workos_core.notification_owner_sequences
    WHERE owner_user_id = sqlc.arg(owner_user_id)
), 0)::bigint AS watermark;

-- name: GetOwnerSweptThrough :one
SELECT swept_through FROM workos_core.notification_owner_sequences
WHERE owner_user_id = sqlc.arg(owner_user_id);

-- name: ListNotificationsPage :many
SELECT id, owner_user_id, project_id, kind, severity, origin, title, body,
       target_kind, target_id, app_id, app_installation_id, source_process,
       source_id, source_digest, created_at, read_at, read_change_sequence, created_change_sequence
FROM workos_core.notifications
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.narg('project_id') ::uuid IS NULL OR project_id = sqlc.narg('project_id') ::uuid)
  AND (NOT sqlc.arg(unread_only) OR read_at IS NULL)
  AND (sqlc.narg('kind') ::text IS NULL OR kind = sqlc.narg('kind') ::text)
  AND (sqlc.narg('cursor_created') ::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg('cursor_created') ::timestamptz, sqlc.narg('cursor_id') ::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetChangesAfter :many
SELECT c.change_sequence, c.notification_id, c.change_type, c.revision,
       n.project_id, n.kind, n.severity, n.origin, n.title, n.body,
       n.target_kind, n.target_id, n.app_id, n.app_installation_id,
       n.source_process, n.source_id, n.source_digest, n.created_at,
       n.created_change_sequence, n.read_at,
       n.read_change_sequence
FROM workos_core.notification_changes AS c
JOIN workos_core.notifications AS n
  ON n.id = c.notification_id AND n.owner_user_id = c.owner_user_id
WHERE c.owner_user_id = sqlc.arg(owner_user_id)
  AND c.change_sequence > sqlc.arg(after_sequence)
ORDER BY c.change_sequence
LIMIT sqlc.arg(row_limit);

-- name: GetNotificationReadRequest :one
SELECT request_digest, result_version, result
FROM workos_core.notification_read_requests
WHERE owner_user_id = sqlc.arg(owner_user_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertNotificationReadRequest :execrows
INSERT INTO workos_core.notification_read_requests (
    owner_user_id, idempotency_key, request_digest, result_version, result, created_at
) VALUES (
    sqlc.arg(owner_user_id), sqlc.arg(idempotency_key), sqlc.arg(request_digest),
    1, sqlc.arg(result), sqlc.arg(created_at)
);

-- name: GetNotificationAppRequest :one
SELECT request_digest, result_version, result
FROM workos_core.notification_app_requests
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_installation_id = sqlc.arg(app_installation_id)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertNotificationAppRequest :execrows
INSERT INTO workos_core.notification_app_requests (
    owner_user_id, app_installation_id, idempotency_key, request_digest,
    result_version, result, created_at
) VALUES (
    sqlc.arg(owner_user_id), sqlc.arg(app_installation_id), sqlc.arg(idempotency_key),
    sqlc.arg(request_digest), 1, sqlc.arg(result), sqlc.arg(created_at)
);

-- name: GetNotificationAppQuotaForUpdate :one
SELECT utc_date, daily_count, burst_window_start, burst_count
FROM workos_core.notification_app_quotas
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_installation_id = sqlc.arg(app_installation_id)
FOR UPDATE;

-- name: InsertNotificationAppQuota :execrows
INSERT INTO workos_core.notification_app_quotas (
    owner_user_id, app_installation_id, utc_date, daily_count,
    burst_window_start, burst_count, updated_at
) VALUES (
    sqlc.arg(owner_user_id), sqlc.arg(app_installation_id), sqlc.arg(utc_date),
    0, sqlc.arg(burst_window_start), 0, sqlc.arg(updated_at)
)
ON CONFLICT (owner_user_id, app_installation_id) DO NOTHING;

-- name: UpdateNotificationAppQuota :execrows
UPDATE workos_core.notification_app_quotas
SET utc_date = sqlc.arg(utc_date), daily_count = sqlc.arg(daily_count),
    burst_window_start = sqlc.arg(burst_window_start), burst_count = sqlc.arg(burst_count),
    updated_at = sqlc.arg(updated_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND app_installation_id = sqlc.arg(app_installation_id);

-- Bounded sweep: only already-read notifications older than the cutoff are
-- candidates. Recent unread facts are never swept.
-- name: SelectSweepableNotifications :many
SELECT id, owner_user_id FROM workos_core.notifications
WHERE read_at IS NOT NULL AND read_at < sqlc.arg(cutoff)
ORDER BY read_at, id
LIMIT sqlc.arg(max_batch);

-- name: DeleteNotificationChangesFor :execrows
DELETE FROM workos_core.notification_changes
WHERE notification_id = ANY (sqlc.arg(ids)::uuid[]);

-- name: DeleteNotifications :execrows
DELETE FROM workos_core.notifications
WHERE id = ANY (sqlc.arg(ids)::uuid[]);

-- name: AdvanceOwnerSweptThrough :execrows
UPDATE workos_core.notification_owner_sequences
SET swept_through = GREATEST(swept_through, sqlc.arg(swept_through)), updated_at = sqlc.arg(updated_at)
WHERE owner_user_id = sqlc.arg(owner_user_id);

-- name: MaxChangeSequenceForNotifications :many
SELECT owner_user_id, max(change_sequence)::bigint AS max_seq
FROM workos_core.notification_changes
WHERE notification_id = ANY (sqlc.arg(ids)::uuid[])
GROUP BY owner_user_id;

-- Receipts outlive notifications (longer horizon, same bounded sweep) so a
-- pathological late replay can never project a second notification.
-- name: DeleteOldSourceReceipts :execrows
DELETE FROM workos_core.notification_source_receipts
WHERE recorded_at < sqlc.arg(cutoff);

-- name: GetOwnerLastSequence :one
SELECT last_sequence FROM workos_core.notification_owner_sequences
WHERE owner_user_id = sqlc.arg(owner_user_id);
