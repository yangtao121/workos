-- Owner: runtime-host. Persist launch identity and separate provenance from byte deduplication.
ALTER TABLE workos_runtime.workloads
    ADD COLUMN artifact_id text NOT NULL DEFAULT '',
    ADD COLUMN artifact_digest text NOT NULL DEFAULT '',
    ADD CONSTRAINT workloads_bundle_identity CHECK (
        (artifact_id = '' AND artifact_digest = '') OR
        (artifact_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
         AND artifact_digest ~ '^sha256:[0-9a-f]{64}$'));
ALTER TABLE workos_runtime.artifacts DROP CONSTRAINT artifacts_owner_user_id_digest_key;
CREATE INDEX artifacts_content_idx ON workos_runtime.artifacts(owner_user_id, digest);
CREATE UNIQUE INDEX artifacts_build_task_unique ON workos_runtime.artifacts(task_id)
    WHERE origin = 'build_job';
