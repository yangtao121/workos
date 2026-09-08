-- Owner: Core App Registry (ADR-0026). Staged repair candidate versions are
-- invisible to default selection and owner-driven transitions until
-- PublishRepairCandidateVersion flips them after the canary window.
ALTER TABLE workos_core.app_versions
    ADD COLUMN state text NOT NULL DEFAULT 'published';

ALTER TABLE workos_core.app_versions
    ADD CONSTRAINT app_versions_state_check CHECK (state IN ('staged', 'published'));

-- Durable mapping from one completed repair task to its staged version.
-- Registration is idempotent per task; a task can never stage two versions.
CREATE TABLE workos_core.app_repair_candidate_versions (
    task_id uuid PRIMARY KEY CHECK (task_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    incident_id uuid NOT NULL,
    build_job_id uuid NOT NULL,
    source_digest text NOT NULL CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    app_version_id uuid NOT NULL UNIQUE,
    published_at timestamptz,
    created_at timestamptz NOT NULL,
    FOREIGN KEY (owner_user_id, app_version_id)
        REFERENCES workos_core.app_versions (owner_user_id, id)
);
