-- Owner: workos-core Artifact. Honest app provenance, without synthetic tasks.
ALTER TABLE workos_core.project_review_artifacts
    ALTER COLUMN source_task_id DROP NOT NULL,
    ADD COLUMN source_app_instance_id uuid,
    ADD CONSTRAINT project_review_artifacts_one_source CHECK (
        (source_task_id IS NOT NULL) <> (source_app_instance_id IS NOT NULL)
    ),
    ADD CONSTRAINT project_review_artifacts_app_binding_unique
        UNIQUE (id, owner_user_id, project_id, source_app_instance_id, output_key);

CREATE TABLE workos_core.app_review_artifact_requests (
    app_instance_id uuid NOT NULL,
    output_key text NOT NULL CHECK (output_key ~ '^[a-z][a-z0-9._-]{0,63}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (app_instance_id, output_key),
    FOREIGN KEY (artifact_id, owner_user_id, project_id, app_instance_id, output_key)
        REFERENCES workos_core.project_review_artifacts
            (id, owner_user_id, project_id, source_app_instance_id, output_key)
        DEFERRABLE INITIALLY DEFERRED
);
