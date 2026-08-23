CREATE TABLE workos_core.app_versions (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    app_id text NOT NULL CHECK (app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    version text NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
    scope text NOT NULL CHECK (scope IN ('user', 'project')),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    permissions text[] NOT NULL DEFAULT '{}',
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    canonical_manifest jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, app_id, version),
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX app_versions_owner_app_idx
    ON workos_core.app_versions (owner_user_id, app_id);
