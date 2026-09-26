-- Runtime-owned resident native workload identity and attachment generation.
-- Old attachments belong to generation 1; new attaches record the resolved
-- workload generation so a restarted workload cannot accept stale viewers.
ALTER TABLE workos_runtime.surface_attachments
  ADD COLUMN workload_generation bigint NOT NULL DEFAULT 1
  CHECK (workload_generation > 0);

ALTER TABLE workos_runtime.native_sessions
  ADD COLUMN child_container_id text,
  ADD COLUMN child_image_id text,
  ADD COLUMN child_generation bigint,
  ADD CONSTRAINT native_child_identity_pair CHECK (
    (child_container_id IS NULL AND child_image_id IS NULL AND child_generation IS NULL)
    OR (child_container_id IS NOT NULL AND child_image_id IS NOT NULL AND child_generation > 0)
  );
