-- Owner: runtime-host Build/Test executor (ADR-0026). Durable job ledger;
-- never a Go map. One job per repair task, bounded retries, restart recovery.
CREATE SCHEMA IF NOT EXISTS workos_runtime;

CREATE TABLE workos_runtime.build_jobs (
    id uuid PRIMARY KEY CHECK (id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    task_id uuid NOT NULL UNIQUE CHECK (task_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    incident_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    installation_id uuid NOT NULL,
    input_digest text NOT NULL CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_bundle_id uuid NOT NULL,
    source_digest text NOT NULL CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    base_image text NOT NULL CHECK (char_length(base_image) BETWEEN 1 AND 256),
    -- The exact immutable job payload: input + candidate files (bounded by
    -- ADR-0024 limits), pinned at submit for replay verification.
    payload jsonb NOT NULL,
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    stage text NOT NULL DEFAULT 'submit'
        CHECK (stage IN ('submit', 'materialize', 'build', 'test', 'verify')),
    build_exit_code int4,
    test_exit_code int4,
    failure_reason text
        CHECK (failure_reason IS NULL OR failure_reason IN
               ('build-failed', 'test-failed', 'timeout', 'engine-failed',
                'output-budget', 'input-drift', 'cancelled')),
    engine_facts jsonb,
    attempts int4 NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    log_tail text CHECK (log_tail IS NULL OR octet_length(log_tail) <= 65536),
    lease_owner text,
    lease_until timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX build_jobs_reconcile_idx
    ON workos_runtime.build_jobs (state, updated_at);
