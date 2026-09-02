-- 030_reliability_notification_publications.sql
-- Owner: reliability-host (internal/reliability).
--
-- Durable notification publication facts (ADR-0014). A publication is the
-- reliability-host-side authority that an incident fact exists and should be
-- projected into the Core notification stream. Publications carry identity,
-- scope, severity, and a versioned digest — never raw observations,
-- telemetry, engine output, or content. Core consumes them over the private
-- IncidentNotificationPublicationSourceService with at-least-once leases;
-- Core records its durable receipt before completing, so a lost completion
-- replays as a no-op against that receipt. Core never queries this schema
-- with SQL, and no other process writes these tables.
--
-- Forward-only: migrations 001-029 stay byte-identical.

-- The composite FK target: incidents are owner-bound facts, so the
-- publication binding proves the (id, owner) pair, not just the id.
CREATE UNIQUE INDEX IF NOT EXISTS incidents_id_owner_unique
    ON workos_reliability.incidents (id, owner_user_id);

CREATE TABLE workos_reliability.notification_publications (
    id uuid PRIMARY KEY,
    -- The incident this publication projects. The composite FK binds the
    -- publication to the same-owner incident fact.
    incident_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    -- Finite severity category at publication time (info/warning/critical).
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    -- Finite action outcome category at publication time; the notification
    -- records this snapshot, later mitigation never rewrites it.
    action_outcome text NOT NULL CHECK (action_outcome IN (
        'pending', 'restarted', 'stopped', 'failed'
    )),
    -- Versioned canonical digest of the publication fields; Core treats
    -- same id + different digest as contract violation, never as an update.
    digest text NOT NULL CHECK (digest LIKE 'sha256:%'),
    -- Reliability transaction time of the incident fact (UTC).
    occurred_at timestamptz NOT NULL,
    -- Claim lease facts: internal Core worker identity + opaque server-minted
    -- claim token + deadline. An expired lease may be re-claimed.
    claim_locked_by text,
    claim_token text,
    claim_locked_until timestamptz,
    claim_attempts integer NOT NULL DEFAULT 0 CHECK (claim_attempts >= 0),
    -- Terminal consumption fact. NULL means pending.
    outcome text CHECK (outcome IN ('completed')),
    completed_at timestamptz,
    completed_by text,
    created_at timestamptz NOT NULL,
    CONSTRAINT notification_publications_incident_fk
        FOREIGN KEY (incident_id, owner_user_id)
        REFERENCES workos_reliability.incidents (id, owner_user_id)
);

-- One notification publication per incident: the incident is inserted
-- exactly once, so any second publication attempt is a physical violation.
CREATE UNIQUE INDEX notification_publications_incident_unique
    ON workos_reliability.notification_publications (incident_id);

-- FIFO claim ordering over pending publications only; consumers claim with
-- FOR UPDATE SKIP LOCKED.
CREATE INDEX notification_publications_claim_idx
    ON workos_reliability.notification_publications (occurred_at, id)
    WHERE outcome IS NULL;

COMMENT ON TABLE workos_reliability.notification_publications IS
    'owner: reliability-host; durable incident notification publications (no content)';
COMMENT ON COLUMN workos_reliability.notification_publications.claim_locked_by IS
    'internal Core consumer identity; never browser-supplied';
COMMENT ON COLUMN workos_reliability.notification_publications.claim_token IS
    'opaque server-minted claim secret; never logged or returned to public callers';
