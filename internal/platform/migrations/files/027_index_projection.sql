-- 027_index_projection.sql
-- Owner: indexer (internal/indexer).
--
-- The indexer's own durable projection (ADR-0013): a rebuildable search
-- projection, at-least-once consumption receipts, consumer cursors, durable
-- repair jobs, and shadow-generation rebuild facts. No table here is read or
-- written by Core through SQL; there are no foreign keys into or out of this
-- schema. Every row is derivable from Core authority, so the whole schema is
-- disposable for disaster recovery while Core stays untouched.
--
-- Forward-only: migrations 001-026 stay byte-identical.

CREATE SCHEMA workos_index;

-- ---------------------------------------------------------------------------
-- Search projection, generation-scoped so a full rebuild builds a shadow
-- generation while the previous completed generation keeps serving searches.
-- ---------------------------------------------------------------------------

CREATE TABLE workos_index.documents (
    projection_generation uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    source_type text NOT NULL CHECK (source_type = 'artifact.review.v1'),
    -- Immutable review artifact identity from Core authority.
    source_id uuid NOT NULL,
    source_digest text NOT NULL CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_type text NOT NULL CHECK (artifact_type IN ('document.markdown.v1', 'code.unified-diff.v1')),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    -- Canonical bounded content projection (<= 512 KiB). It is the ranking
    -- and excerpt basis; it never leaves the indexer except as bounded
    -- plain-text excerpts.
    content text NOT NULL CHECK (length(content) <= 524288),
    -- Deterministic lexical representation: the built-in 'simple'
    -- configuration, no extra extensions, no locale-dependent dictionaries.
    title_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', title)) STORED,
    body_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    source_created_at timestamptz NOT NULL,
    -- Ordering fact: the last publication that touched this document.
    last_publication_id uuid NOT NULL,
    source_operation text NOT NULL CHECK (source_operation IN ('review-artifact.upsert')),
    indexed_at timestamptz NOT NULL,
    -- Tombstone keeps the physical row for audit/rebuild comparison but
    -- removes it from search; tombstone order is monotonic per document.
    tombstoned_at timestamptz,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (projection_generation, owner_user_id, project_id, source_id),
    CONSTRAINT documents_tombstone_order CHECK (tombstoned_at IS NULL OR tombstoned_at >= indexed_at)
);

CREATE INDEX documents_search_idx
    ON workos_index.documents USING gin (body_tsv);
CREATE INDEX documents_title_search_idx
    ON workos_index.documents USING gin (title_tsv);
-- Owner/project scan ordering for pagination tie-breaks and reconciliation.
CREATE INDEX documents_scope_idx
    ON workos_index.documents (projection_generation, owner_user_id, project_id, source_created_at, source_id);

-- ---------------------------------------------------------------------------
-- Publication receipts: exactly-once local effect per (publication,
-- generation), decided by this primary key inside the same local transaction
-- as the document effect. Core delivery stays at-least-once; the receipt
-- turns replays into no-ops.
-- ---------------------------------------------------------------------------

CREATE TABLE workos_index.publication_receipts (
    publication_id uuid NOT NULL,
    projection_generation uuid NOT NULL,
    -- Canonical request digest: same publication + same digest replays as a
    -- no-op; same publication + different digest is corruption.
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    outcome text NOT NULL CHECK (outcome IN ('applied', 'tombstoned', 'unsupported', 'corrupt')),
    source_digest text CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$' OR source_digest IS NULL),
    processed_at timestamptz NOT NULL,
    PRIMARY KEY (publication_id, projection_generation)
);

-- ---------------------------------------------------------------------------
-- Consumer state: durable high-watermark of consumed publications plus the
-- observed Core snapshot watermark from the last completed reconciliation.
-- ---------------------------------------------------------------------------

CREATE TABLE workos_index.consumer_state (
    worker_id text PRIMARY KEY,
    cursor_publication_id uuid,
    cursor_occurred_at timestamptz,
    observed_watermark text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL
);

-- ---------------------------------------------------------------------------
-- Owner-triggered repair/reindex jobs (IndexContext). The request mapping is
-- the durable idempotency authority (same key + same canonical request
-- replays the exact first response; different request conflicts; failures
-- never consume the key).
-- ---------------------------------------------------------------------------

CREATE TABLE workos_index.index_job_requests (
    owner_user_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);

CREATE TABLE workos_index.index_jobs (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'running', 'completed', 'failed')),
    failure_category text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE INDEX index_jobs_pending_idx
    ON workos_index.index_jobs (created_at, id) WHERE state IN ('pending', 'running');

CREATE TABLE workos_index.index_job_sources (
    job_id uuid NOT NULL REFERENCES workos_index.index_jobs (id),
    artifact_id uuid NOT NULL,
    expected_digest text NOT NULL CHECK (expected_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'skipped')),
    outcome text,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (job_id, artifact_id)
);

-- ---------------------------------------------------------------------------
-- Shadow-generation rebuild facts (ADR-0013 §9). Searches read only the
-- active generation; a rebuild builds a shadow generation, catches it up to
-- the live delta, validates it, and promotes it in one compare-and-swap.
-- ---------------------------------------------------------------------------

CREATE TABLE workos_index.projection_generations (
    id uuid PRIMARY KEY,
    -- 'project' or 'all'.
    scope text NOT NULL CHECK (scope IN ('project', 'all')),
    owner_user_id uuid,
    project_id uuid,
    status text NOT NULL CHECK (status IN ('building', 'active', 'retired', 'failed', 'canceled')),
    snapshot_boundary text NOT NULL DEFAULT '',
    document_count bigint NOT NULL DEFAULT 0,
    tombstone_count bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL,
    promoted_at timestamptz,
    retired_at timestamptz,
    CONSTRAINT generations_project_scope_shape CHECK (
        (scope = 'project' AND owner_user_id IS NOT NULL AND project_id IS NOT NULL)
        OR (scope = 'all')
    )
);

-- The single pointer every search reads. Promotion is a single-row CAS:
-- a stale or failed worker cannot overwrite a later successful promotion.
CREATE TABLE workos_index.active_generation (
    singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    generation_id uuid NOT NULL REFERENCES workos_index.projection_generations (id)
);

CREATE TABLE workos_index.rebuild_jobs (
    id uuid PRIMARY KEY,
    scope text NOT NULL CHECK (scope IN ('project', 'all')),
    owner_user_id uuid,
    project_id uuid,
    idempotency_digest text NOT NULL CHECK (idempotency_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('requested', 'snapshotting', 'catching_up', 'validating', 'promoting', 'completed', 'canceled', 'failed')),
    target_generation uuid NOT NULL REFERENCES workos_index.projection_generations (id),
    -- Durable resume facts: the snapshot page cursor, the snapshot boundary,
    -- and running counts. A restart resumes from these, never from zero.
    phase_cursor text NOT NULL DEFAULT '',
    snapshot_boundary text NOT NULL DEFAULT '',
    source_count bigint NOT NULL DEFAULT 0,
    applied_count bigint NOT NULL DEFAULT 0,
    tombstone_count bigint NOT NULL DEFAULT 0,
    failure_category text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    terminal_at timestamptz,
    CONSTRAINT rebuild_project_scope_shape CHECK (
        (scope = 'project' AND owner_user_id IS NOT NULL AND project_id IS NOT NULL)
        OR (scope = 'all')
    )
);

-- One live rebuild per scope, decided by the database.
CREATE UNIQUE INDEX rebuild_jobs_live_scope_unique
    ON workos_index.rebuild_jobs (
        scope, COALESCE(owner_user_id, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid)
    )
    WHERE state IN ('requested', 'snapshotting', 'catching_up', 'validating', 'promoting');

CREATE TABLE workos_index.rebuild_job_requests (
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    job_id uuid NOT NULL REFERENCES workos_index.rebuild_jobs (id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (idempotency_key)
);

COMMENT ON SCHEMA workos_index IS
    'owner: indexer; rebuildable search projection, receipts, jobs, generations';
COMMENT ON TABLE workos_index.documents IS
    'owner: indexer; generation-scoped lexical document projection (no content leaves except bounded excerpts)';
COMMENT ON TABLE workos_index.publication_receipts IS
    'owner: indexer; exactly-once local effect per (publication, generation)';
COMMENT ON TABLE workos_index.active_generation IS
    'owner: indexer; single-row pointer every search reads; CAS-promoted';

-- Project tombstone facts: once an archive is consumed, late or replayed
-- upserts for the same owner+project are recorded as tombstoned receipts and
-- can never resurrect documents.
CREATE TABLE workos_index.project_tombstones (
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    last_publication_id uuid NOT NULL,
    archived_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, project_id)
);
