-- 012: surface session installation grant revision snapshot
-- (owner: runtime-host Surface).
--
-- The runtime must persist the installation grant epoch that Core's private
-- ResolveWebBundle returned at session creation (ADR-0003): effective
-- capabilities are the create-time snapshot, and every later private
-- authorization compares this stored epoch against Core's current
-- installation grant revision. Existing sessions backfill to 1 — the only
-- epoch that existed before grant mutation.
--
-- The column stays a plain runtime-owned snapshot fact: there is deliberately
-- NO foreign key into workos_core and no query of Core tables; revocation is
-- decided solely by revision comparison on the private RPC path.

ALTER TABLE workos_runtime.surface_sessions
    ADD COLUMN installation_grant_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT surface_sessions_installation_grant_revision_positive
        CHECK (installation_grant_revision >= 1);
