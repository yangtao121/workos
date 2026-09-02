-- 029_core_notifications.sql
-- Owner: workos-core Notification (internal/core/notification).
--
-- Durable notification facts, the monotonic owner change stream, read state,
-- source receipts, read-command idempotency, and the app notification
-- quota/idempotency authorities (ADR-0014). Agent/Artifact/Project modules
-- never touch these tables directly: same-process producers append through
-- the neutral tx-scoped sink port, runtime-host reaches Core only through
-- the private ingest RPC, and reliability-host publications arrive through
-- the at-least-once claim/complete channel. Titles/bodies are server-derived
-- bounded inert plain text from finite templates; task goals, provider
-- output, artifact content, incident raw telemetry, credentials, and
-- workspace URIs never enter these tables.
--
-- Forward-only: migrations 001-028 stay byte-identical.

CREATE TABLE workos_core.notifications (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    -- Optional project scope; global agent tasks carry NULL. App-origin
    -- notifications are always project-scoped (CHECK below).
    project_id uuid REFERENCES workos_core.projects (id),
    kind text NOT NULL CHECK (kind IN (
        'agent.approval.required',
        'agent.task.terminal',
        'artifact.review.created',
        'reliability.incident.opened',
        'app.instance.message'
    )),
    severity text NOT NULL CHECK (severity IN ('normal', 'critical')),
    origin text NOT NULL CHECK (origin IN ('system', 'app')),
    -- Server-derived bounded inert plain text (code points and byte bounds
    -- enforced here and revalidated on every read).
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120
        AND octet_length(title) <= 512),
    body text NOT NULL DEFAULT '' CHECK (char_length(body) <= 500
        AND octet_length(body) <= 2048),
    -- Finite typed action target. target_kind names the authoritative
    -- surface; target_id is the canonical UUIDv7 of the approval / task /
    -- artifact / incident / installation. There is deliberately no URL,
    -- route, or method column.
    target_kind text NOT NULL CHECK (target_kind IN (
        'approval', 'task', 'artifact', 'incident', 'app'
    )),
    target_id uuid NOT NULL,
    -- Registry app id for app-origin notifications; NULL for system facts.
    app_id text,
    -- App-origin binding: the installation that created the fact. System
    -- facts never carry one.
    app_installation_id uuid,
    -- Source binding for exactly-once projection. source_id is the stable
    -- identity of the producing source fact (approval id / task id /
    -- artifact id / publication id / app request id); source_digest is the
    -- versioned canonical digest of the source fields.
    source_process text NOT NULL CHECK (source_process IN (
        'workos-core', 'reliability-host'
    )),
    source_id text NOT NULL,
    source_digest text NOT NULL CHECK (source_digest LIKE 'sha256:%'),
    created_at timestamptz NOT NULL,
    -- Monotonic read projection: NULL = unread, and once set never cleared.
    read_at timestamptz,
    -- Change sequence of the READ change; 0 while unread.
    read_change_sequence bigint NOT NULL DEFAULT 0 CHECK (read_change_sequence >= 0),
    -- Kind/target coherence: each kind maps to exactly one target shape,
    -- and app-origin facts must be fully app-bound while system facts never
    -- are.
    CONSTRAINT notifications_kind_target_shape CHECK (
        (kind = 'agent.approval.required' AND target_kind = 'approval'
            AND origin = 'system' AND app_id IS NULL AND app_installation_id IS NULL)
        OR (kind = 'agent.task.terminal' AND target_kind = 'task'
            AND origin = 'system' AND app_id IS NULL AND app_installation_id IS NULL)
        OR (kind = 'artifact.review.created' AND target_kind = 'artifact'
            AND origin = 'system' AND app_id IS NULL AND app_installation_id IS NULL)
        OR (kind = 'reliability.incident.opened' AND target_kind = 'incident'
            AND origin = 'system' AND app_id IS NULL AND app_installation_id IS NULL)
        OR (kind = 'app.instance.message' AND target_kind = 'app'
            AND origin = 'app' AND project_id IS NOT NULL
            AND app_id IS NOT NULL AND app_installation_id IS NOT NULL)
    ),
    -- Read coherence: read facts always carry their change sequence and
    -- vice versa.
    CONSTRAINT notifications_read_coherence CHECK (
        (read_at IS NULL AND read_change_sequence = 0)
        OR (read_at IS NOT NULL AND read_change_sequence > 0)
    )
);

-- One notification per source fact: the exactly-once projection arbiter.
CREATE UNIQUE INDEX notifications_source_unique
    ON workos_core.notifications (source_process, source_id);

-- Owner lists (newest first) and unread counts.
CREATE INDEX notifications_owner_created_idx
    ON workos_core.notifications (owner_user_id, created_at DESC, id DESC);
CREATE INDEX notifications_owner_unread_idx
    ON workos_core.notifications (owner_user_id)
    WHERE read_at IS NULL;

COMMENT ON TABLE workos_core.notifications IS
    'owner: workos-core Notification; durable owner-scoped notification facts';

CREATE TABLE workos_core.notification_changes (
    owner_user_id uuid NOT NULL,
    -- Strictly increasing per-owner change sequence with no gaps in normal
    -- operation (allocation rolls back with its transaction).
    change_sequence bigint NOT NULL CHECK (change_sequence > 0),
    notification_id uuid NOT NULL,
    change_type text NOT NULL CHECK (change_type IN ('created', 'read')),
    -- Notification revision after this change (== change_sequence).
    revision bigint NOT NULL,
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, change_sequence)
);

-- Per-notification change lookup for cursor replay.
CREATE INDEX notification_changes_notification_idx
    ON workos_core.notification_changes (owner_user_id, notification_id);

COMMENT ON TABLE workos_core.notification_changes IS
    'owner: workos-core Notification; monotonic owner-wide change stream';

CREATE TABLE workos_core.notification_owner_sequences (
    owner_user_id uuid PRIMARY KEY REFERENCES workos_core.users (id),
    -- Last allocated change sequence.
    last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    -- Authoritative stream-gap watermark: changes at or below this sequence
    -- were removed by the bounded sweep; cursors at or below it must be
    -- answered with RESET_REQUIRED, never silently skipped.
    swept_through bigint NOT NULL DEFAULT 0 CHECK (swept_through >= 0),
    updated_at timestamptz NOT NULL
);

COMMENT ON TABLE workos_core.notification_owner_sequences IS
    'owner: workos-core Notification; per-owner change sequence counter and sweep watermark';

CREATE TABLE workos_core.notification_source_receipts (
    source_process text NOT NULL CHECK (source_process IN (
        'workos-core', 'reliability-host'
    )),
    source_id text NOT NULL,
    source_digest text NOT NULL,
    -- Logical reference to the projected notification. Deliberately no FK:
    -- receipts must outlive the bounded notification retention so a
    -- pathological late replay can never project a second notification.
    -- Receipts are retired by the same bounded sweep on a longer horizon.
    notification_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL,
    PRIMARY KEY (source_process, source_id)
);

COMMENT ON TABLE workos_core.notification_source_receipts IS
    'owner: workos-core Notification; exactly-once projection receipts';

CREATE TABLE workos_core.notification_read_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    -- Versioned canonical request digest; same key/different digest is a
    -- stable conflict, never a silent reinterpretation.
    request_digest text NOT NULL CHECK (request_digest LIKE 'sha256:%'),
    result_version integer NOT NULL DEFAULT 1 CHECK (result_version = 1),
    -- Versioned first-response snapshot (read change sequence, unread
    -- count, and the per-notification read facts).
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key)
);

COMMENT ON TABLE workos_core.notification_read_requests IS
    'owner: workos-core Notification; durable read-command idempotency';

CREATE TABLE workos_core.notification_app_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    app_installation_id uuid NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    -- Versioned canonical request digest over the bounded app input.
    request_digest text NOT NULL CHECK (request_digest LIKE 'sha256:%'),
    result_version integer NOT NULL DEFAULT 1 CHECK (result_version = 1),
    -- Versioned first-response snapshot (notification + change facts).
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    -- The namespace is per installation: two apps (or two installations)
    -- never conflict on the same client key.
    PRIMARY KEY (owner_user_id, app_installation_id, idempotency_key)
);

COMMENT ON TABLE workos_core.notification_app_requests IS
    'owner: workos-core Notification; durable app create idempotency';

CREATE TABLE workos_core.notification_app_quotas (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    app_installation_id uuid NOT NULL,
    utc_date date NOT NULL,
    daily_count integer NOT NULL DEFAULT 0 CHECK (daily_count >= 0),
    -- Short-window burst facts; window_start anchors the current burst
    -- bucket (UTC), burst_count the reservations inside it.
    burst_window_start timestamptz NOT NULL,
    burst_count integer NOT NULL DEFAULT 0 CHECK (burst_count >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, app_installation_id)
);

COMMENT ON TABLE workos_core.notification_app_quotas IS
    'owner: workos-core Notification; atomic UTC daily + burst quota buckets';
