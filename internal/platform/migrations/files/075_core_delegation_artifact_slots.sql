-- Owner: core Artifact. Each Core-authorized delegation has its own result
-- slot; ordinary task outputs retain the original one-per-requested-type rule.
ALTER TABLE workos_core.project_review_artifact_outputs
 ADD COLUMN delegation_id text NOT NULL DEFAULT ''
 CHECK (delegation_id = '' OR delegation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$');
DROP INDEX workos_core.project_review_artifact_outputs_task_type_unique;
CREATE UNIQUE INDEX project_review_artifact_outputs_task_scope_type_unique
 ON workos_core.project_review_artifact_outputs(task_id, delegation_id, artifact_type);
