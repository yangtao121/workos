-- 013: authoritative project create-request idempotency (owner: workos-core
-- Project; ADR-0004).
--
-- projects.idempotency_key with UNIQUE (owner_user_id, idempotency_key) can
-- only prove that a key created a project once; it cannot prove that a replay
-- carries the same canonical request, and the mutable project row can never
-- represent the first Create response after any later Update/Archive. This
-- migration adds the dedicated authority the create command adjudicates
-- against:
--   * (owner_user_id, idempotency_key) is the primary key: the consumed-key
--     namespace is owner-scoped and the primary key arbitrates same-key races
--     across processes exactly like project_app_installation_requests;
--   * request_digest pins the canonical client request (versioned sha256);
--   * result pins the versioned first-response Project snapshot, so replays
--     return the exact first response even after the project row mutated.
--
-- The projects table keeps its existing data and constraints unchanged; its
-- UNIQUE (owner_user_id, idempotency_key) index remains the physical insert
-- arbiter. Rows created before this migration have no request digest or
-- result snapshot and history is not fabricable: their keys are treated as
-- legacy and any create replay against them fails closed (Aborted) in the
-- repository — no backfill, no guessed digests.

CREATE TABLE workos_core.project_create_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object' AND result ->> 'result_version' = '1'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);
