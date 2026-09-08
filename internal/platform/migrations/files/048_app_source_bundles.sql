-- Owner: Core App Registry. Immutable build inputs, never host filesystem paths.
CREATE TABLE workos_core.app_source_bundles (
    id uuid PRIMARY KEY CHECK (id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    files jsonb NOT NULL CHECK (jsonb_typeof(files) = 'array' AND jsonb_array_length(files) BETWEEN 1 AND 128),
    total_size_bytes bigint NOT NULL CHECK (total_size_bytes BETWEEN 0 AND 524288),
    created_at timestamptz NOT NULL,
    UNIQUE (owner_user_id, idempotency_key)
);
