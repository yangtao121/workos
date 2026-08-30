-- 017: incident acknowledge idempotency (owner: reliability-host; ADR-0006).
--
-- The acknowledge RPC carries a durable idempotency key; this migration
-- gives that key a persisted, uniqueness-enforced home. The key makes the
-- external write exactly-once per (owner, key): same key replays the same
-- acknowledged state across caller and process restarts, and a key reused on
-- a different incident is a stable conflict. The acknowledgement state
-- itself (acknowledged_at) stays a one-way fact independent of any key.

ALTER TABLE workos_reliability.incidents
    ADD COLUMN acknowledge_key text CHECK (char_length(acknowledge_key) BETWEEN 1 AND 128);

CREATE UNIQUE INDEX incidents_owner_acknowledge_key_idx
    ON workos_reliability.incidents (owner_user_id, acknowledge_key)
    WHERE acknowledge_key IS NOT NULL;
