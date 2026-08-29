-- Reliability Incident persistence queries (reliability-host owned tables
-- only; the runtime schema is never queried).

-- name: InsertIncident :execrows
-- The occurrence_digest unique key is the at-least-once arbiter: a replayed
-- episode inserts nothing and the caller reads the stored row instead.
INSERT INTO workos_reliability.incidents (
    id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
    workload_generation, violation, severity, summary, occurrence_digest,
    evidence_digest, state, restart_outcome, revision, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(project_id), sqlc.arg(app_instance_id),
    sqlc.arg(app_id), sqlc.arg(workload_id), sqlc.arg(workload_generation),
    sqlc.arg(violation), sqlc.arg(severity), sqlc.arg(summary), sqlc.arg(occurrence_digest),
    sqlc.arg(evidence_digest), sqlc.arg(state), sqlc.arg(restart_outcome),
    sqlc.arg(revision), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (occurrence_digest) DO NOTHING;

-- name: GetIncident :one
SELECT id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
       workload_generation, violation, severity, summary, occurrence_digest,
       evidence_digest, state, restart_outcome, revision,
       acknowledged_at, mitigated_at, resolved_at, created_at, updated_at
FROM workos_reliability.incidents
WHERE id = sqlc.arg(id);

-- name: ListIncidentsPage :many
-- Owner-scoped, project-optional, keyed pagination on (created_at, id). The
-- caller probes limit+1 rows so a full final page never phantom-pages.
SELECT i.id, i.owner_user_id, i.project_id, i.app_instance_id, i.app_id, i.workload_id,
       i.workload_generation, i.violation, i.severity, i.summary, i.occurrence_digest,
       i.evidence_digest, i.state, i.restart_outcome, i.revision,
       i.acknowledged_at, i.mitigated_at, i.resolved_at, i.created_at, i.updated_at
FROM workos_reliability.incidents i
WHERE i.owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.arg(project_id)::text = '' OR i.project_id = sqlc.arg(project_id)::uuid)
  AND (
    sqlc.arg(page_token)::text = ''
    OR (i.created_at, i.id) > (
      SELECT p.created_at, p.id FROM workos_reliability.incidents p
      WHERE p.id = sqlc.arg(page_token)::uuid
    )
  )
ORDER BY i.created_at, i.id
LIMIT sqlc.arg(row_limit);

-- name: UpdateIncidentOutcome :execrows
UPDATE workos_reliability.incidents SET
    state = sqlc.arg(state),
    restart_outcome = sqlc.arg(restart_outcome),
    mitigated_at = sqlc.arg(mitigated_at),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND state = 'open';

-- name: MarkIncidentResolved :execrows
UPDATE workos_reliability.incidents SET
    state = 'resolved',
    resolved_at = sqlc.arg(resolved_at),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND state IN ('open', 'mitigated');

-- name: AcknowledgeIncident :execrows
-- The owner acknowledgement is a separate fact from mitigation and never
-- claims the fault is repaired; repeat acknowledges are no-ops.
UPDATE workos_reliability.incidents SET
    acknowledged_at = sqlc.arg(acknowledged_at),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND owner_user_id = sqlc.arg(owner_user_id)
  AND acknowledged_at IS NULL;

-- name: ListOpenIncidentsForWorkload :many
SELECT id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
       workload_generation, violation, severity, summary, occurrence_digest,
       evidence_digest, state, restart_outcome, revision,
       acknowledged_at, mitigated_at, resolved_at, created_at, updated_at
FROM workos_reliability.incidents
WHERE workload_id = sqlc.arg(workload_id)
  AND workload_generation = sqlc.arg(workload_generation)
  AND state IN ('open', 'mitigated')
ORDER BY created_at, id;

-- name: UpsertIncidentAction :exec
INSERT INTO workos_reliability.incident_actions (
    incident_id, action, action_key, outcome, result_generation,
    created_at, updated_at
) VALUES (
    sqlc.arg(incident_id), sqlc.arg(action), sqlc.arg(action_key), sqlc.arg(outcome),
    sqlc.arg(result_generation), sqlc.arg(created_at), sqlc.arg(updated_at)
)
ON CONFLICT (incident_id, action) DO UPDATE
SET outcome = sqlc.arg(outcome),
    result_generation = sqlc.arg(result_generation),
    updated_at = sqlc.arg(updated_at);

-- name: GetIncidentAction :one
SELECT incident_id, action, action_key, outcome, result_generation, created_at, updated_at
FROM workos_reliability.incident_actions
WHERE incident_id = sqlc.arg(incident_id) AND action = sqlc.arg(action);

-- name: LoadSupervisorProgress :one
SELECT workload_id, generation, last_state, last_health, last_exit,
       last_restart_count, stable_polls, exit_occurrence, health_occurrence,
       oom_occurrence, pids_occurrence, first_seen_at, updated_at
FROM workos_reliability.supervisor_workloads
WHERE workload_id = sqlc.arg(workload_id);

-- name: UpsertSupervisorProgress :exec
INSERT INTO workos_reliability.supervisor_workloads (
    workload_id, generation, last_state, last_health, last_exit,
    last_restart_count, stable_polls, exit_occurrence, health_occurrence,
    oom_occurrence, pids_occurrence, first_seen_at, updated_at
) VALUES (
    sqlc.arg(workload_id), sqlc.arg(generation), sqlc.arg(last_state), sqlc.arg(last_health),
    sqlc.arg(last_exit), sqlc.arg(last_restart_count), sqlc.arg(stable_polls),
    sqlc.arg(exit_occurrence), sqlc.arg(health_occurrence), sqlc.arg(oom_occurrence),
    sqlc.arg(pids_occurrence), sqlc.arg(first_seen_at), sqlc.arg(updated_at)
)
ON CONFLICT (workload_id) DO UPDATE
SET generation = sqlc.arg(generation),
    last_state = sqlc.arg(last_state),
    last_health = sqlc.arg(last_health),
    last_exit = sqlc.arg(last_exit),
    last_restart_count = sqlc.arg(last_restart_count),
    stable_polls = sqlc.arg(stable_polls),
    exit_occurrence = sqlc.arg(exit_occurrence),
    health_occurrence = sqlc.arg(health_occurrence),
    oom_occurrence = sqlc.arg(oom_occurrence),
    pids_occurrence = sqlc.arg(pids_occurrence),
    updated_at = sqlc.arg(updated_at);

-- name: GetSupervisorCheckpoint :one
SELECT id, last_poll_at, updated_at
FROM workos_reliability.supervisor_checkpoints
WHERE id = 'supervisor';

-- name: UpsertSupervisorCheckpoint :exec
INSERT INTO workos_reliability.supervisor_checkpoints (id, last_poll_at, updated_at)
VALUES ('supervisor', sqlc.arg(last_poll_at), sqlc.arg(updated_at))
ON CONFLICT (id) DO UPDATE
SET last_poll_at = sqlc.arg(last_poll_at),
    updated_at = sqlc.arg(updated_at);
