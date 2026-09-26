-- Gateway-owned single-owner password credentials and device sessions.
-- Pairing facts remain intact for deployments that select pairing mode.
CREATE TABLE workos_gateway.password_credentials (
    owner_user_id uuid PRIMARY KEY,
    username text NOT NULL CHECK (char_length(username) BETWEEN 1 AND 80),
    password_hash text NOT NULL CHECK (password_hash LIKE '$argon2id$v=19$%'),
    revision bigint NOT NULL CHECK (revision >= 1),
    updated_at timestamptz NOT NULL
);

CREATE TABLE workos_gateway.password_devices (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'),
    device_class text NOT NULL CHECK (device_class IN ('desktop', 'tablet', 'foldable', 'phone')),
    revision bigint NOT NULL CHECK (revision >= 1),
    created_at timestamptz NOT NULL,
    last_authenticated_at timestamptz NOT NULL,
    revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    UNIQUE (id, owner_user_id)
);
CREATE INDEX password_devices_owner_idx ON workos_gateway.password_devices(owner_user_id, id);

CREATE TABLE workos_gateway.password_sessions (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    last_seen_at timestamptz CHECK (last_seen_at IS NULL OR last_seen_at >= created_at),
    revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    FOREIGN KEY (device_id, owner_user_id)
        REFERENCES workos_gateway.password_devices(id, owner_user_id)
);
CREATE INDEX password_sessions_owner_idx ON workos_gateway.password_sessions(owner_user_id, device_id);

CREATE TABLE workos_gateway.password_device_revocations (
    owner_user_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);
