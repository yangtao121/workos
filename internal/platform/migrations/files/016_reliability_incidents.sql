-- 016: reliability-host incidents and supervision progress
-- (owner: reliability-host Supervisor / Incident Manager; ADR-0006).
--
-- Reliability owns its own facts: the durable Incident ledger, the
-- per-occurrence idempotency that keeps at-least-once observation replays
-- from double-reporting, the bounded action ledger that keeps at-least-once
-- decision execution from double-restarting, and the poll checkpoint. The
-- rows never reference runtime-host tables and never query them: workloads
-- are observed and controlled exclusively through the private, versioned
-- SupervisedWorkloadService RPCs, and every stored reference is a plain
-- snapshot fact.
--
--   incidents               one row per (workload, generation, violation,
--                           occurrence) — enforced by a digest unique key;
--   incident_actions        one row per (incident, action): the idempotency
--                           key sent to the runtime for restart/terminate;
--   supervisor_checkpoints  single-row poll cursor for the supervisor loop;
--   supervisor_workloads    per-workload supervision progress (last observed
--                           facts, stable streak, occurrence ordinals).

CREATE SCHEMA IF NOT EXISTS workos_reliability;

CREATE TABLE workos_reliability.incidents (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    app_instance_id uuid NOT NULL,
    app_id text NOT NULL CHECK (app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    workload_id uuid NOT NULL,
    workload_generation bigint NOT NULL CHECK (workload_generation >= 1),
    violation text NOT NULL CHECK (violation IN (
        'unexpected_exit', 'health_failure', 'oom', 'pids_limit', 'restart_limit_exhausted')),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    -- summary is a fixed phrase mapped from the violation enum at the
    -- application boundary; raw engine output, HTTP bodies, logs, and user
    -- content never enter this column.
    summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 200),
    occurrence_digest text NOT NULL CHECK (occurrence_digest ~ '^sha256:[0-9a-f]{64}$'),
    -- evidence_digest is the sha256 of the bounded observation snapshot that
    -- triggered the incident; it proves what was seen without carrying it.
    evidence_digest text NOT NULL CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('open', 'mitigated', 'resolved')),
    restart_outcome text NOT NULL DEFAULT 'pending' CHECK (restart_outcome IN ('pending', 'restarted', 'stopped', 'failed')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision >= 1),
    acknowledged_at timestamptz,
    mitigated_at timestamptz,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (occurrence_digest),
    CONSTRAINT incidents_coherent_mitigation CHECK (
        (state = 'open' OR mitigated_at IS NOT NULL)
        AND (state <> 'resolved' OR (mitigated_at IS NOT NULL AND resolved_at IS NOT NULL AND resolved_at >= mitigated_at))
    )
);

CREATE INDEX incidents_owner_project_idx
    ON workos_reliability.incidents (owner_user_id, project_id, created_at DESC, id DESC);

CREATE TABLE workos_reliability.incident_actions (
    incident_id uuid NOT NULL,
    action text NOT NULL CHECK (action IN ('restart', 'terminate')),
    action_key text NOT NULL CHECK (char_length(action_key) BETWEEN 1 AND 128),
    -- outcome is the sanitized verdict replayed from the runtime control RPC:
    -- 'restarted' (with the new generation) or 'stopped' on success;
    -- 'limit_exhausted', 'conflict', 'unavailable', or 'failed' on refusal.
    outcome text NOT NULL CHECK (outcome IN ('restarted', 'stopped', 'limit_exhausted', 'conflict', 'unavailable', 'failed')),
    result_generation bigint CHECK (result_generation IS NULL OR result_generation >= 1),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (incident_id, action)
);

CREATE TABLE workos_reliability.supervisor_checkpoints (
    id text PRIMARY KEY CHECK (id = 'supervisor'),
    last_poll_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- supervisor_workloads is the per-workload supervision progress: the last
-- observed state and health verdict, the stable-poll streak behind
-- resolution, and the occurrence ordinals that keep at-least-once replays
-- from double-reporting the same violation episode.
CREATE TABLE workos_reliability.supervisor_workloads (
    workload_id uuid PRIMARY KEY,
    generation bigint NOT NULL CHECK (generation >= 1),
    last_state text NOT NULL CHECK (last_state IN ('pending', 'starting', 'running', 'stopping', 'stopped', 'failed', 'unknown')),
    last_health text NOT NULL DEFAULT 'unknown' CHECK (last_health IN ('unknown', 'ok', 'failing')),
    last_exit text NOT NULL DEFAULT 'none' CHECK (last_exit IN ('none', 'exited', 'oom', 'pids', 'unknown')),
    last_restart_count integer NOT NULL DEFAULT 0 CHECK (last_restart_count >= 0),
    stable_polls integer NOT NULL DEFAULT 0 CHECK (stable_polls >= 0),
    exit_occurrence integer NOT NULL DEFAULT 0 CHECK (exit_occurrence >= 0),
    health_occurrence integer NOT NULL DEFAULT 0 CHECK (health_occurrence >= 0),
    oom_occurrence integer NOT NULL DEFAULT 0 CHECK (oom_occurrence >= 0),
    pids_occurrence integer NOT NULL DEFAULT 0 CHECK (pids_occurrence >= 0),
    first_seen_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);
