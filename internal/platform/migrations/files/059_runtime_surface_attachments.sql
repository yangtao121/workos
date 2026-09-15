-- Owner: runtime-host surface continuity facts (ADR-0031). Attachments are
-- per-device access relations to supervised workloads; control is a
-- single-controller epoch advanced only by explicit takeover.
CREATE TABLE workos_runtime.surface_attachments (
    attachment_id uuid PRIMARY KEY CHECK (attachment_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    workload_id uuid NOT NULL,
    surface_session_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    device_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    controls boolean NOT NULL DEFAULT false,
    control_generation bigint NOT NULL DEFAULT 0 CHECK (control_generation >= 0),
    state text NOT NULL CHECK (state IN ('attached', 'detached', 'expired')),
    attached_at timestamptz NOT NULL,
    control_expires_at timestamptz,
    detached_at timestamptz,
    UNIQUE (owner_user_id, idempotency_key)
);

CREATE INDEX surface_attachments_live_idx
    ON workos_runtime.surface_attachments (workload_id, state);

CREATE INDEX surface_attachments_surface_idx
    ON workos_runtime.surface_attachments (surface_session_id);

-- Per-workload control ledger: exactly one live controller row per workload.
-- Takeover bumps control_generation here and invalidates the previous
-- controller's attachment row in the same transaction.
CREATE TABLE workos_runtime.surface_control_leases (
    workload_id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    control_generation bigint NOT NULL DEFAULT 1 CHECK (control_generation > 0),
    controller_attachment_id uuid NOT NULL,
    controller_device_id uuid NOT NULL,
    granted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);
