-- owner: core
-- Preserve old attempts without asserting that their relay accepted delivery.
ALTER TABLE workos_core.push_deliveries
    ALTER COLUMN delivered_at DROP NOT NULL,
    ADD COLUMN state text NOT NULL DEFAULT 'unknown'
        CHECK (state IN ('unknown', 'pending', 'delivered', 'suppressed', 'failed')),
    ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 8),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN claim_token uuid;
UPDATE workos_core.push_deliveries SET delivered_at = NULL;
CREATE INDEX push_deliveries_pending ON workos_core.push_deliveries(next_attempt_at)
    WHERE state = 'pending';
COMMENT ON TABLE workos_core.push_deliveries IS
    'owner: core; transactional wake outbox, leased retries with at-least-once relay delivery';
