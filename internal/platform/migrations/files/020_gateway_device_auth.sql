-- 020: Gateway-owned device authentication (owner: workos-gateway; ADR-0007).
--
-- All production device credentials, pairing state, proof challenges, device
-- sessions, and revocation idempotency belong to the Access Gateway. The
-- schema never references Core, Runtime, or Reliability tables: cross-process
-- identity flows exclusively through the Gateway-injected trusted headers.
-- The legacy workos_core.devices scaffold from 001 is deliberately untouched
-- and remains a development-only fact; it is not a production authority and
-- is never promoted or backfilled.
--
-- Secrets are never persisted in plaintext:
--   * pairing ticket secrets, session tokens, and public keys are stored
--     only as domain-separated SHA-256 digests in the canonical
--     "sha256:<64 lowercase hex>" grammar (the SPKI DER itself is kept for
--     signature verification — it is public material, not a secret);
--   * state machines are enforced by guarded UPDATEs in the owning service,
--     and by CHECK constraints here so a buggy writer cannot persist an
--     incoherent fact.
--
--   device_credentials           one durable browser profile credential per
--                                paired device (server-minted UUIDv7 id,
--                                canonical P-256 SPKI DER, digest, revision);
--   pairing_tickets              short-lived single-use pairing invitations
--                                (secret hash, public origin + TLS leaf
--                                fingerprint snapshot, bounded attempts); a
--                                partial unique index allows at most one
--                                pending ticket per owner; the claimed device
--                                identity carries no foreign key because it
--                                names a credential that is only inserted
--                                when the pairing proof completes — completion
--                                binding is adjudicated by the application
--                                transaction against the locked ticket and
--                                challenge rows, and the credentials table's
--                                unique (owner, key digest) index forbids
--                                duplicates;
--   device_auth_challenges       single-use proof challenges for pairing and
--                                session purposes (32-byte nonce, bounded
--                                attempts, one consumption verdict); session
--                                challenges for unknown devices are stored
--                                with a NULL device_id so they can burn
--                                attempts without creating an existence
--                                oracle; device_id carries no foreign key
--                                because a pairing challenge precedes the
--                                credential row it names — binding integrity
--                                comes from the ticket row's composite FK;
--   device_sessions              opaque bearer sessions (token hash only,
--                                absolute expiry, revocation); a partial
--                                unique index allows at most one active
--                                session per device;
--   device_revocation_requests   owner-scoped idempotency authority for
--                                RevokeDevice: same key + same digest replays
--                                the immutable first result snapshot.

CREATE SCHEMA workos_gateway;

CREATE TABLE workos_gateway.device_credentials (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND name !~ '[[:cntrl:]]'),
    device_class text NOT NULL CHECK (device_class IN ('desktop', 'tablet', 'foldable', 'phone')),
    public_key_spki bytea NOT NULL CHECK (octet_length(public_key_spki) BETWEEN 1 AND 256),
    public_key_hash text NOT NULL CHECK (public_key_hash ~ '^sha256:[0-9a-f]{64}$'),
    revision bigint NOT NULL CHECK (revision >= 1),
    created_at timestamptz NOT NULL,
    last_authenticated_at timestamptz NOT NULL,
    revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    UNIQUE (id, owner_user_id)
);

-- At most one active (non-revoked) credential per owner and browser profile
-- key: a completed pairing can never be duplicated by replaying the same
-- ticket against a second claim.
CREATE UNIQUE INDEX device_credentials_owner_key_active_idx
    ON workos_gateway.device_credentials (owner_user_id, public_key_hash)
    WHERE revoked_at IS NULL;

CREATE INDEX device_credentials_owner_idx
    ON workos_gateway.device_credentials (owner_user_id, id);

CREATE TABLE workos_gateway.pairing_tickets (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    secret_hash text NOT NULL UNIQUE CHECK (secret_hash ~ '^sha256:[0-9a-f]{64}$'),
    public_origin text NOT NULL,
    tls_fingerprint text NOT NULL CHECK (tls_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('pending', 'claimed', 'completed', 'revoked')),
    device_id uuid,
    public_key_hash text CHECK (public_key_hash IS NULL OR public_key_hash ~ '^sha256:[0-9a-f]{64}$'),
    claimed_name text CHECK (claimed_name IS NULL OR (char_length(claimed_name) BETWEEN 1 AND 80 AND claimed_name !~ '[[:cntrl:]]')),
    claimed_class text CHECK (claimed_class IS NULL OR claimed_class IN ('desktop', 'tablet', 'foldable', 'phone')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 10),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    claimed_at timestamptz,
    completed_at timestamptz,
    revoked_at timestamptz,
    CONSTRAINT pairing_tickets_pending_facts_check CHECK ((state = 'pending') = (claimed_at IS NULL AND revoked_at IS NULL)),
    CONSTRAINT pairing_tickets_claimed_facts_check CHECK ((state = 'claimed') = (claimed_at IS NOT NULL AND completed_at IS NULL AND revoked_at IS NULL)),
    CONSTRAINT pairing_tickets_completed_facts_check CHECK ((state = 'completed') = (completed_at IS NOT NULL)),
    CONSTRAINT pairing_tickets_revoked_facts_check CHECK ((state = 'revoked') = (revoked_at IS NOT NULL)),
    -- A pending ticket never carries claim facts; a claimed or completed
    -- ticket always carries all of them. A revoked ticket may be either
    -- (revoked while pending by rotation, or revoked after a claim), so no
    -- bidirectional constraint is imposed on it.
    CONSTRAINT pairing_tickets_pending_no_claim_check CHECK (state <> 'pending' OR (device_id IS NULL AND public_key_hash IS NULL AND claimed_name IS NULL AND claimed_class IS NULL)),
    CONSTRAINT pairing_tickets_claim_has_facts_check CHECK (state NOT IN ('claimed', 'completed') OR (device_id IS NOT NULL AND public_key_hash IS NOT NULL AND claimed_name IS NOT NULL AND claimed_class IS NOT NULL)),
    CONSTRAINT pairing_tickets_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT pairing_tickets_claimed_after_created_check CHECK (claimed_at IS NULL OR claimed_at >= created_at),
    CONSTRAINT pairing_tickets_completed_after_created_check CHECK (completed_at IS NULL OR completed_at >= created_at),
    CONSTRAINT pairing_tickets_revoked_after_created_check CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

-- At most one outstanding (pending) ticket per owner; rotation revokes the
-- previous ticket inside the owner-level database lock before inserting.
CREATE UNIQUE INDEX pairing_tickets_owner_pending_idx
    ON workos_gateway.pairing_tickets (owner_user_id)
    WHERE state = 'pending';

CREATE TABLE workos_gateway.device_auth_challenges (
    id uuid PRIMARY KEY,
    purpose text NOT NULL CHECK (purpose IN ('pairing', 'session')),
    device_id uuid,
    ticket_id uuid,
    public_key_hash text NOT NULL CHECK (public_key_hash ~ '^sha256:[0-9a-f]{64}$'),
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 32),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 10),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_by_device uuid,
    result text CHECK (result IN ('verified', 'failed')),
    CHECK ((purpose = 'pairing') = (ticket_id IS NOT NULL)),
    CHECK ((purpose = 'session') = (ticket_id IS NULL)),
    CHECK (result IS NULL OR consumed_at IS NOT NULL),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CHECK (expires_at > created_at),
    FOREIGN KEY (ticket_id) REFERENCES workos_gateway.pairing_tickets (id)
);

CREATE TABLE workos_gateway.device_sessions (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at),
    last_seen_at timestamptz CHECK (last_seen_at IS NULL OR last_seen_at >= created_at),
    revoked_at timestamptz CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    FOREIGN KEY (device_id, owner_user_id)
        REFERENCES workos_gateway.device_credentials (id, owner_user_id)
);

-- At most one active session per device: a new proof atomically revokes the
-- previous session inside the device row lock, so a lost response can never
-- leave two usable bearer tokens.
CREATE UNIQUE INDEX device_sessions_device_active_idx
    ON workos_gateway.device_sessions (device_id)
    WHERE revoked_at IS NULL;

CREATE INDEX device_sessions_owner_idx
    ON workos_gateway.device_sessions (owner_user_id, device_id);

CREATE TABLE workos_gateway.device_revocation_requests (
    owner_user_id uuid NOT NULL,
    idempotency_key text NOT NULL
        CHECK (idempotency_key ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    result jsonb NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    result_version text NOT NULL CHECK (result_version = 'v1'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);
