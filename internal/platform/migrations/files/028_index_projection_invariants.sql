-- 028_index_projection_invariants.sql
-- Owner: indexer (internal/indexer).
--
-- A deployment has one global search-generation pointer. Consequently a
-- rebuild request with project scope records operator intent, but constructs
-- a complete Core-authoritative generation before promotion. Only one live
-- rebuild and one active generation may exist, and live publications mirror
-- into every building generation.

DROP INDEX workos_index.rebuild_jobs_live_scope_unique;

CREATE UNIQUE INDEX rebuild_jobs_single_live_unique
    ON workos_index.rebuild_jobs ((true))
    WHERE state IN ('requested', 'snapshotting', 'catching_up', 'validating', 'promoting');

CREATE UNIQUE INDEX projection_generations_single_building_unique
    ON workos_index.projection_generations ((true))
    WHERE status = 'building';

CREATE UNIQUE INDEX projection_generations_single_active_unique
    ON workos_index.projection_generations ((true))
    WHERE status = 'active';

ALTER TABLE workos_index.documents
    ADD CONSTRAINT documents_content_octet_bounds
    CHECK (octet_length(content) BETWEEN 1 AND 524288);

ALTER TABLE workos_index.projection_generations
    DROP CONSTRAINT generations_project_scope_shape,
    ADD CONSTRAINT generations_project_scope_shape CHECK (
        (scope = 'project' AND owner_user_id IS NOT NULL AND project_id IS NOT NULL)
        OR (scope = 'all' AND owner_user_id IS NULL AND project_id IS NULL)
    );

ALTER TABLE workos_index.rebuild_jobs
    DROP CONSTRAINT rebuild_project_scope_shape,
    ADD CONSTRAINT rebuild_project_scope_shape CHECK (
        (scope = 'project' AND owner_user_id IS NOT NULL AND project_id IS NOT NULL)
        OR (scope = 'all' AND owner_user_id IS NULL AND project_id IS NULL)
    );
