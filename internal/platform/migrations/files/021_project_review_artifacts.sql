-- 021: immutable project review artifacts (owner: workos-core Artifact; ADR-0008).
--
-- This migration adds the review artifact subtype: canonical, bounded,
-- read-only Markdown or unified-diff documents materialized by a Project
-- Agent under an active task lease. It does not modify any earlier
-- migration; the web bundle subtype from 006 keeps its owner-scoped
-- semantics and its bytes stay private.
--
--   workos_core.project_review_artifacts
--       one immutable review artifact row: server-minted UUIDv7 identity,
--       owner, project/task provenance, canonical type, normalized title,
--       content digest, bounded counts, and the exact canonical UTF-8
--       content bytes. Content is user data and is never logged.
--   workos_core.project_review_artifact_outputs
--       the durable (task, output key) adjudication mapping. Its primary
--       key arbitrates provider retries and lost responses: the identical
--       canonical request replays the first artifact and its published
--       timeline event, any different request on the same key is a stable
--       conflict verdict. The unique (task_id, artifact_type) index enforces
--       at most one materialized artifact per requested type and task.
--       event_id / event_sequence / event_occurred_at are the Core-minted
--       publication record written in the same transaction as the artifact
--       row; the timeline event itself stays owned by the Agent module's
--       event stream, this row only records the publication reference so a
--       replay can return exactly the first event without re-publishing.
--
-- There are deliberately no foreign keys into the Agent or Project tables:
-- cross-module facts are validated through neutral ports at run time, and
-- provenance columns are stable server-derived snapshots.

CREATE TABLE workos_core.project_review_artifacts (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    type text NOT NULL CHECK (type IN ('document.markdown.v1', 'code.unified-diff.v1')),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    media_type text NOT NULL CHECK (media_type IN ('text/markdown; charset=utf-8', 'text/x-diff; charset=utf-8')),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    project_id uuid NOT NULL,
    source_task_id uuid NOT NULL,
    output_key text NOT NULL CHECK (output_key ~ '^[a-z][a-z0-9._-]{0,63}$'),
    byte_count integer NOT NULL CHECK (byte_count BETWEEN 1 AND 524288),
    line_count integer NOT NULL CHECK (line_count BETWEEN 1 AND 20000),
    content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 524288),
    created_at timestamptz NOT NULL,
    CONSTRAINT project_review_artifacts_owner_id_unique UNIQUE (owner_user_id, id),
    CONSTRAINT project_review_artifacts_type_media_match CHECK (
        (type = 'document.markdown.v1' AND media_type = 'text/markdown; charset=utf-8')
        OR (type = 'code.unified-diff.v1' AND media_type = 'text/x-diff; charset=utf-8')
    )
);

CREATE TABLE workos_core.project_review_artifact_outputs (
    task_id uuid NOT NULL,
    output_key text NOT NULL CHECK (output_key ~ '^[a-z][a-z0-9._-]{0,63}$'),
    artifact_type text NOT NULL CHECK (artifact_type IN ('document.markdown.v1', 'code.unified-diff.v1')),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    event_id uuid NOT NULL,
    event_sequence bigint NOT NULL CHECK (event_sequence > 0),
    event_occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (task_id, output_key),
    CONSTRAINT project_review_artifact_outputs_artifact_fkey
        FOREIGN KEY (artifact_id)
        REFERENCES workos_core.project_review_artifacts (id)
        ON DELETE RESTRICT
);

-- At most one materialized artifact per (task, requested type): a provider
-- emitting the same type under a second output key fails closed here.
CREATE UNIQUE INDEX project_review_artifact_outputs_task_type_unique
    ON workos_core.project_review_artifact_outputs (task_id, artifact_type);

-- Project-scoped listing reads the owner's review artifacts in UUIDv7 order.
CREATE INDEX project_review_artifacts_project_idx
    ON workos_core.project_review_artifacts (owner_user_id, project_id, id);
