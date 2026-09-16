-- 066_runtime_artifacts.sql
-- Owner: runtime-host release bundle repository (ADR-0033). Metadata for
-- immutable app-bundle.v1 release bundles; the bytes live only in the
-- runtime-owned on-disk store. One row per (owner, content digest); state
-- transitions are reconciled against the real files on startup.
CREATE SCHEMA IF NOT EXISTS workos_runtime;

CREATE TABLE workos_runtime.artifacts (
    id uuid PRIMARY KEY CHECK (id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    format text NOT NULL CHECK (format IN ('app-bundle.v1')),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    file_count int4 NOT NULL CHECK (file_count > 0),
    -- preparing: atomic write window; ready: file verified on disk;
    -- failed: corrupt/incomplete, never a launch input;
    -- unavailable: ready metadata but the bytes are gone (ADR-0033 section 6).
    state text NOT NULL CHECK (state IN ('preparing', 'ready', 'failed', 'unavailable')),
    origin text NOT NULL CHECK (origin IN ('build_job', 'operator_import')),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    -- Operator import binding (always set for operator_import).
    app_id text CHECK (app_id IS NULL OR app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    -- Build job provenance (always set for build_job).
    task_id uuid,
    job_id uuid,
    incident_id uuid,
    project_id uuid,
    installation_id uuid,
    source_bundle_id uuid,
    source_digest text CHECK (source_digest IS NULL OR source_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_digest text CHECK (manifest_digest IS NULL OR manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    base_image text CHECK (base_image IS NULL OR char_length(base_image) BETWEEN 1 AND 256),
    build_command text[],
    test_command text[],
    output_directory text CHECK (output_directory IS NULL OR char_length(output_directory) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL,
    ready_at timestamptz,
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, digest),
    UNIQUE (owner_user_id, origin, idempotency_key)
);

CREATE INDEX artifacts_owner_state_idx ON workos_runtime.artifacts (owner_user_id, state);
CREATE INDEX artifacts_task_idx ON workos_runtime.artifacts (task_id) WHERE task_id IS NOT NULL;

-- The producing job pins its committed bundle; the lease-guarded verdict
-- write is the only path allowed to set it (ADR-0033 section 6).
ALTER TABLE workos_runtime.build_jobs
    ADD COLUMN artifact_id uuid,
    ADD COLUMN artifact_digest text
        CHECK (artifact_digest IS NULL OR artifact_digest ~ '^sha256:[0-9a-f]{64}$');
