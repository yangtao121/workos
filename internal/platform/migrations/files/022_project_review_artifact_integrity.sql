-- 022: tighten immutable Project review artifact integrity (owner:
-- workos-core Artifact).
--
-- Migration 021 may already be present on an acceptance volume and is
-- checksum-protected, so these additional invariants are forward-only. They
-- keep the durable output adjudication bound to the exact Artifact-owned row
-- and make a stored byte-count drift impossible even before application read
-- revalidation. No foreign key crosses into Project, Agent, or event tables.

ALTER TABLE workos_core.project_review_artifacts
    ADD CONSTRAINT project_review_artifacts_content_size_match
        CHECK (byte_count = octet_length(content)),
    ADD CONSTRAINT project_review_artifacts_created_at_finite
        CHECK (isfinite(created_at)),
    ADD CONSTRAINT project_review_artifacts_full_binding_unique
        UNIQUE (id, owner_user_id, project_id, source_task_id, output_key, type);

ALTER TABLE workos_core.project_review_artifact_outputs
    DROP CONSTRAINT project_review_artifact_outputs_artifact_fkey,
    ADD CONSTRAINT project_review_artifact_outputs_full_binding_fkey
        FOREIGN KEY (
            artifact_id, owner_user_id, project_id, task_id, output_key, artifact_type
        )
        REFERENCES workos_core.project_review_artifacts (
            id, owner_user_id, project_id, source_task_id, output_key, type
        )
        ON DELETE RESTRICT,
    ADD CONSTRAINT project_review_artifact_outputs_event_time_finite
        CHECK (isfinite(event_occurred_at)),
    ADD CONSTRAINT project_review_artifact_outputs_created_at_finite
        CHECK (isfinite(created_at));
