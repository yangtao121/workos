-- 010: surface bridge token facts (owner: runtime-host Surface Broker).
--
-- The bridge token is a random 256-bit CSPRNG secret; only its sha256 digest
-- is persisted here (ADR-0002), verified with a constant-time comparison.
--   * bridge_token_hash IS NULL means "no currently valid token": every
--     successful CreateSurface (fresh create or open replay) mints a new
--     token and overwrites the hash, immediately invalidating the previous
--     one; CloseSurface clears it; a closed/expired session replay never
--     mints. Token expiry equals the session's expires_at, so no separate
--     expiry column exists;
--   * binding facts are the session row itself (owner_user_id, device_id,
--     project_id, app_instance_id): verification always resolves the row by
--     owner + hash and then still requires the trusted device to match;
--   * bridge_capabilities stores the effective capability list computed at
--     create time (requested ∩ granted ∩ implemented, canonical sorted);
--     canonicality is enforced by the owning application service, arrays
--     cannot carry subquery CHECK constraints;
--   * the rows stay runtime-owned: no reference to or query of Core schema.

ALTER TABLE workos_runtime.surface_sessions
    ADD COLUMN bridge_token_hash text CHECK (bridge_token_hash ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN bridge_capabilities text[] NOT NULL DEFAULT '{}';

CREATE INDEX surface_sessions_bridge_token_idx
    ON workos_runtime.surface_sessions (owner_user_id, bridge_token_hash)
    WHERE bridge_token_hash IS NOT NULL;
