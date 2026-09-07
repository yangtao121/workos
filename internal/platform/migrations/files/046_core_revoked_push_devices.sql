-- owner: workos-core
-- Tombstones prevent late in-flight subscribe requests from reviving a device.
CREATE TABLE workos_core.revoked_push_devices (
    owner_user_id uuid NOT NULL,
    device_id uuid NOT NULL,
    revoked_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, device_id)
);
