-- 026_core_index_publications.sql
-- Owner: workos-core Index Feed (internal/core/indexfeed).
--
-- Durable index publication facts (ADR-0013). A publication is the Core-side
-- authority that a review artifact became index-source (or a project was
-- archived); it never carries artifact content, task goals, provider output,
-- credentials, workspace URIs, or user display names. The indexer consumes
-- these facts over the private IndexPublicationSourceService with
-- at-least-once leases; this table is never read or written by the indexer
-- through SQL, and no other Core module queries it except through the
-- tx-scoped neutral sink port.
--
-- Forward-only: migrations 001-025 stay byte-identical.

CREATE TABLE workos_core.index_publications (
    id uuid PRIMARY KEY,
    operation text NOT NULL CHECK (operation IN ('review-artifact.upsert', 'project.tombstone')),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    source_type text NOT NULL CHECK (source_type = 'artifact.review.v1'),
    -- Review artifact id; NULL for project tombstones.
    source_id uuid,
    -- Review subtype (document.markdown.v1 / code.unified-diff.v1); NULL for
    -- tombstones.
    artifact_type text,
    -- Exact sha256 digest; NULL for tombstones.
    digest text,
    -- Core transaction time of the source mutation (UTC microseconds).
    occurred_at timestamptz NOT NULL,
    -- Claim lease facts. The lease owner is an internal indexer worker
    -- identity, never a browser-supplied value. The claim token is an
    -- opaque server-minted secret proving the live claim; a stale complete
    -- can never satisfy a new lease. An expired lease may be re-claimed.
    claim_locked_by text,
    claim_token text,
    claim_locked_until timestamptz,
    claim_attempts integer NOT NULL DEFAULT 0 CHECK (claim_attempts >= 0),
    -- Terminal consumption fact. NULL means pending. Outcomes distinguish
    -- successful consumption from observable degraded outcomes
    -- (unsupported/corrupt); transient outages stay retryable and are never
    -- recorded here.
    outcome text CHECK (outcome IN ('completed', 'tombstoned', 'unsupported', 'corrupt')),
    completed_at timestamptz,
    completed_by text,
    created_at timestamptz NOT NULL,
    -- Upsert facts must be internally consistent when present.
    CONSTRAINT index_publications_upsert_shape CHECK (
        (operation = 'review-artifact.upsert' AND source_id IS NOT NULL
            AND artifact_type IS NOT NULL AND digest IS NOT NULL)
        OR (operation = 'project.tombstone' AND source_id IS NULL
            AND artifact_type IS NULL AND digest IS NULL)
    )
);

-- One authoritative upsert publication per immutable review artifact: the
-- artifact is inserted exactly once, so any second upsert attempt for the
-- same source id is a physical violation, not a business event.
CREATE UNIQUE INDEX index_publications_upsert_source_unique
    ON workos_core.index_publications (source_id)
    WHERE operation = 'review-artifact.upsert';

-- One tombstone publication per project lifetime: archive is a guarded
-- one-time transition (archived_at IS NULL guard), so replayed or stale
-- archive revisions can never mint a second tombstone.
CREATE UNIQUE INDEX index_publications_tombstone_project_unique
    ON workos_core.index_publications (project_id)
    WHERE operation = 'project.tombstone';

-- Claim ordering: FIFO by source mutation time, then id, for pending
-- publications only. Consumers claim through the private service with
-- FOR UPDATE SKIP LOCKED.
CREATE INDEX index_publications_claim_idx
    ON workos_core.index_publications (occurred_at, id)
    WHERE outcome IS NULL;

COMMENT ON TABLE workos_core.index_publications IS
    'owner: workos-core Index Feed; durable index publication facts (no content)';
COMMENT ON COLUMN workos_core.index_publications.claim_locked_by IS
    'internal indexer worker identity; never browser-supplied';
COMMENT ON COLUMN workos_core.index_publications.claim_token IS
    'opaque server-minted claim secret; never logged or returned to public callers';
