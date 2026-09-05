-- 038_indexer_workspace_sources.sql
-- owner: indexer
-- Workspace file sources (ADR-0017 §4, W4): an owner explicitly binds one
-- local directory per project; the indexer ingests bounded text files as
-- first-class documents with honest provenance (workspace.file.v1), and the
-- mount lifecycle (active/degraded/stopped) is durable state in the same
-- owner schema. Project archive tombstones keep their existing terminal
-- semantics over workspace documents.

-- Widen the document provenance grammar: workspace documents ride the same
-- upsert/receipt/tombstone projection as review artifacts.
ALTER TABLE workos_index.documents DROP CONSTRAINT documents_source_type_check;
ALTER TABLE workos_index.documents
    ADD CONSTRAINT documents_source_type_check
    CHECK (source_type IN ('artifact.review.v1', 'workspace.file.v1'));

ALTER TABLE workos_index.documents DROP CONSTRAINT documents_artifact_type_check;
ALTER TABLE workos_index.documents
    ADD CONSTRAINT documents_artifact_type_check
    CHECK (artifact_type IN ('document.markdown.v1', 'code.unified-diff.v1', 'workspace.text.v1'));

ALTER TABLE workos_index.documents DROP CONSTRAINT documents_source_operation_check;
ALTER TABLE workos_index.documents
    ADD CONSTRAINT documents_source_operation_check
    CHECK (source_operation IN ('review-artifact.upsert', 'workspace.upsert'));

CREATE TABLE workos_index.workspace_sources (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    -- Absolute local mount root as explicitly bound by the owner. Stored as
    -- configured; never a container-internal guess.
    root_path text NOT NULL CHECK (length(root_path) BETWEEN 1 AND 4096),
    -- active: syncs run; degraded: mount failed, syncs stop with the reason
    -- recorded; stopped: owner-unbound, no further syncs.
    status text NOT NULL CHECK (status IN ('active', 'degraded', 'stopped')),
    degraded_reason text NOT NULL DEFAULT '',
    indexed_count bigint NOT NULL DEFAULT 0,
    skipped_count bigint NOT NULL DEFAULT 0,
    tombstoned_count bigint NOT NULL DEFAULT 0,
    last_synced_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, project_id)
);

COMMENT ON TABLE workos_index.workspace_sources IS
    'owner: indexer; owner-bound local directory mounts ingested as workspace documents (ADR-0017 §4)';
