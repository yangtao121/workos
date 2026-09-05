-- 040_indexer_archive_objects.sql
-- owner: indexer
-- Generic archive minimal implementation (ADR-0017 §5): a bounded per-owner
-- object store. 8 MiB per object, content-addressed dedup per owner, and a
-- bounded per-owner object count. The archive is deliberately NOT a
-- knowledge source: objects never enter the search projection.

CREATE TABLE workos_index.archive_objects (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^sha256:[0-9a-f]{64}$'),
    media_type text NOT NULL DEFAULT 'application/octet-stream'
        CHECK (char_length(media_type) BETWEEN 1 AND 128),
    byte_count bigint NOT NULL CHECK (byte_count BETWEEN 1 AND 8388608),
    bytes bytea NOT NULL CHECK (octet_length(bytes) = byte_count),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, sha256)
);

COMMENT ON TABLE workos_index.archive_objects IS
    'owner: indexer; bounded content-addressed archive objects (ADR-0017 §5); never indexed into search';
