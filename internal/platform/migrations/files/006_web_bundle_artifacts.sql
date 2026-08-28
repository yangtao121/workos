-- 006: immutable web bundle artifacts (owner: workos-core Artifact).
-- The artifact tables persist owner-scoped immutable bundle metadata and file
-- bytes plus the durable create-command idempotency mapping. They reference
-- no Project, Registry, or Installation tables: other modules reach bundles
-- only through the Artifact application ports.

CREATE TABLE workos_core.web_bundle_artifacts (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    type text NOT NULL CHECK (type = 'app.web-bundle.v1'),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    media_type text NOT NULL CHECK (media_type = 'application/vnd.workos.web-bundle.v1'),
    content_ref text NOT NULL CHECK (content_ref ~ '^wbbnd:[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    entrypoint text NOT NULL CHECK (entrypoint ~ '^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$' AND entrypoint ~ '\.html$' AND char_length(entrypoint) <= 240),
    file_count integer NOT NULL CHECK (file_count BETWEEN 1 AND 128),
    total_size_bytes bigint NOT NULL CHECK (total_size_bytes BETWEEN 1 AND 2097152),
    created_at timestamptz NOT NULL,
    CONSTRAINT web_bundle_artifacts_owner_id_unique UNIQUE (owner_user_id, id)
);

-- One stored regular file per (artifact, path). The path grammar mirrors the
-- domain normalization: relative POSIX segments starting alphanumeric, no
-- dot/backslash/control characters, bounded length. Media type is derived by
-- the server from the controlled extension table at create time only.
CREATE TABLE workos_core.web_bundle_files (
    artifact_id uuid NOT NULL,
    path text NOT NULL CHECK (path ~ '^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$' AND char_length(path) <= 240),
    media_type text NOT NULL,
    size_bytes integer NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    content bytea NOT NULL,
    PRIMARY KEY (artifact_id, path),
    CONSTRAINT web_bundle_files_artifact_fkey
        FOREIGN KEY (artifact_id)
        REFERENCES workos_core.web_bundle_artifacts (id)
        ON DELETE RESTRICT
);

-- Authoritative create-command idempotency: one persisted result per
-- (owner, idempotency key), bound to the referenced artifact's owner.
CREATE TABLE workos_core.web_bundle_artifact_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_id uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key),
    CONSTRAINT web_bundle_artifact_requests_owner_artifact_fkey
        FOREIGN KEY (owner_user_id, artifact_id)
        REFERENCES workos_core.web_bundle_artifacts (owner_user_id, id)
        ON DELETE RESTRICT
);
