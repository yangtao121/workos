-- Owner: workos-core / desktop. References are validated through module ports,
-- never foreign keys into another module or process's tables.
CREATE TABLE workos_core.desktop_states (
 owner_user_id uuid PRIMARY KEY,
 revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
 state jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (octet_length(state::text) <= 65536)
);
CREATE TABLE workos_core.desktop_operations (
 owner_user_id uuid NOT NULL REFERENCES workos_core.desktop_states(owner_user_id) ON DELETE CASCADE,
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 digest text NOT NULL CHECK (length(digest) = 64),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(owner_user_id,idempotency_key)
);
CREATE TABLE workos_core.desktop_events (
 owner_user_id uuid NOT NULL REFERENCES workos_core.desktop_states(owner_user_id) ON DELETE CASCADE,
 revision bigint NOT NULL CHECK (revision > 0),
 state jsonb NOT NULL CHECK (octet_length(state::text) <= 65536),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(owner_user_id,revision)
);
