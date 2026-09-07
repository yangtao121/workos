-- owner: workos-gateway
-- Device revocation and its Core delivery obligation commit together.
CREATE TABLE workos_gateway.push_revocations (
    device_id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    revoked_at timestamptz NOT NULL,
    delivered_at timestamptz,
    next_attempt_at timestamptz NOT NULL,
    claim_token uuid,
    FOREIGN KEY (device_id, owner_user_id)
        REFERENCES workos_gateway.device_credentials (id, owner_user_id)
);
CREATE INDEX push_revocations_pending_idx
    ON workos_gateway.push_revocations (next_attempt_at, device_id)
    WHERE delivered_at IS NULL;

-- Previously revoked credentials need the same durable propagation.
INSERT INTO workos_gateway.push_revocations (device_id, owner_user_id, revoked_at, next_attempt_at)
SELECT id, owner_user_id, revoked_at, now()
FROM workos_gateway.device_credentials WHERE revoked_at IS NOT NULL;
