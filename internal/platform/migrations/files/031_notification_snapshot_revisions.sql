-- 031_notification_snapshot_revisions.sql
-- Owner: workos-core Notification (internal/core/notification).
--
-- Forward-only review hardening for ADR-0014. Notification.revision is the
-- latest durable change applied to the fact, including the initial CREATED
-- change. Migration 029 stored only the later READ sequence, which made an
-- unread snapshot expose revision zero even though its CREATED change already
-- had a positive revision. Backfill from the authoritative change log before
-- making the invariant mandatory.

ALTER TABLE workos_core.notifications
    ADD COLUMN created_change_sequence bigint;

UPDATE workos_core.notifications AS notification
SET created_change_sequence = created.revision
FROM workos_core.notification_changes AS created
WHERE created.owner_user_id = notification.owner_user_id
  AND created.notification_id = notification.id
  AND created.change_type = 'created';

ALTER TABLE workos_core.notifications
    ALTER COLUMN created_change_sequence SET NOT NULL,
    ADD CONSTRAINT notifications_created_change_sequence_positive
        CHECK (created_change_sequence > 0),
    ADD CONSTRAINT notifications_revision_order
        CHECK (read_change_sequence = 0 OR read_change_sequence > created_change_sequence);

COMMENT ON COLUMN workos_core.notifications.created_change_sequence IS
    'owner: workos-core Notification; immutable CREATED revision for snapshots';
