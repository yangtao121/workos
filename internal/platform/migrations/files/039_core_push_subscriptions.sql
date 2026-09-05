-- 039_core_push_subscriptions.sql
-- owner: core
-- Push wake subscriptions and owner-level delivery preferences (ADR-0018).
-- The relay only ever receives the notification id; durable notification
-- facts stay authoritative regardless of push state.

CREATE TABLE workos_core.push_subscriptions (
    owner_user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    platform text NOT NULL CHECK (platform IN ('web-push', 'fixture')),
    endpoint text NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 2048),
    p256dh text NOT NULL DEFAULT '' CHECK (length(p256dh) <= 512),
    auth_secret text NOT NULL DEFAULT '' CHECK (length(auth_secret) <= 512),
    status text NOT NULL CHECK (status IN ('active', 'revoked')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, device_id, platform)
);

CREATE TABLE workos_core.push_preferences (
    owner_user_id uuid PRIMARY KEY,
    quiet_enabled boolean NOT NULL DEFAULT false,
    quiet_start_utc text NOT NULL DEFAULT '22:00' CHECK (quiet_start_utc ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    quiet_end_utc text NOT NULL DEFAULT '07:00' CHECK (quiet_end_utc ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    updated_at timestamptz NOT NULL
);

-- Bounded delivery log: the idempotency key of relay dispatch, so a replayed
-- notification never wakes a device twice.
CREATE TABLE workos_core.push_deliveries (
    owner_user_id uuid NOT NULL,
    notification_id uuid NOT NULL,
    device_id uuid NOT NULL,
    platform text NOT NULL,
    relay_payload text NOT NULL CHECK (length(relay_payload) <= 1024),
    delivered_at timestamptz NOT NULL,
    PRIMARY KEY (notification_id, device_id, platform)
);

COMMENT ON TABLE workos_core.push_subscriptions IS
    'owner: core; device push wake registrations; relay sees notification ids only (ADR-0018)';
COMMENT ON TABLE workos_core.push_preferences IS
    'owner: core; owner-level quiet hours evaluated server-side before dispatch';
COMMENT ON TABLE workos_core.push_deliveries IS
    'owner: core; relay payload audit + exactly-once dispatch per (notification, device, platform)';
