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
       evidence_digest, state, restart_outcome, revision, acknowledge_key,
       acknowledged_at, mitigated_at, resolved_at, repair_task_id, created_at, updated_at
FROM workos_reliability.incidents
WHERE id = sqlc.arg(id);

-- name: ListIncidentsPage :many
-- Owner-scoped, project-optional, keyed pagination on (created_at, id). The
-- caller probes limit+1 rows so a full final page never phantom-pages.
SELECT i.id, i.owner_user_id, i.project_id, i.app_instance_id, i.app_id, i.workload_id,
       i.workload_generation, i.violation, i.severity, i.summary, i.occurrence_digest,
       i.evidence_digest, i.state, i.restart_outcome, i.revision, i.acknowledge_key,
       i.acknowledged_at, i.mitigated_at, i.resolved_at, i.repair_task_id, i.created_at, i.updated_at
FROM workos_reliability.incidents i
WHERE i.owner_user_id = sqlc.arg(owner_user_id)
  AND (sqlc.arg(project_id)::text = '' OR i.project_id = sqlc.arg(project_id)::uuid)
  AND (
    sqlc.arg(page_token)::text = ''
    OR (i.created_at, i.id) > (
      SELECT p.created_at, p.id FROM workos_reliability.incidents p
      WHERE p.id = sqlc.arg(page_token)::uuid
        AND p.owner_user_id = sqlc.arg(owner_user_id)
        AND (sqlc.arg(project_id)::text = '' OR p.project_id = sqlc.arg(project_id)::uuid)
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
WHERE id = sqlc.arg(id) AND state = 'open' AND restart_outcome = 'pending';

-- name: MarkIncidentResolved :execrows
UPDATE workos_reliability.incidents SET
    state = 'resolved',
    resolved_at = sqlc.arg(resolved_at),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND state = 'mitigated';

-- name: AcknowledgeIncident :execrows
-- The owner acknowledgement is a separate fact from mitigation and never
-- claims the fault is repaired; the idempotency key is persisted so the same
-- key replays the same state and the (owner, key) uniqueness is enforced by
-- the partial unique index from 017. Repeat acknowledges are no-ops.
UPDATE workos_reliability.incidents SET
    acknowledged_at = sqlc.arg(acknowledged_at),
    acknowledge_key = sqlc.arg(acknowledge_key),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND owner_user_id = sqlc.arg(owner_user_id)
  AND acknowledged_at IS NULL;

-- name: IncidentAcknowledgeKeyExists :one
SELECT EXISTS (
    SELECT 1 FROM workos_reliability.incidents
    WHERE owner_user_id = sqlc.arg(owner_user_id)
      AND acknowledge_key = sqlc.arg(acknowledge_key)
      AND id <> sqlc.arg(id)
) AS exists_on_other;

-- name: ListOpenIncidentsForWorkload :many
SELECT id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
       workload_generation, violation, severity, summary, occurrence_digest,
       evidence_digest, state, restart_outcome, revision, acknowledge_key,
       acknowledged_at, mitigated_at, resolved_at, repair_task_id, created_at, updated_at
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
    updated_at = sqlc.arg(updated_at)
-- Only unavailable is retryable. A late/concurrent retry must never erase a
-- terminal action verdict already made authoritative by the runtime key.
WHERE workos_reliability.incident_actions.outcome = 'unavailable';

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

-- name: GetIncidentByOccurrence :one
SELECT id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
       workload_generation, violation, severity, summary, occurrence_digest,
       evidence_digest, state, restart_outcome, revision, acknowledge_key,
       acknowledged_at, mitigated_at, resolved_at, repair_task_id, created_at, updated_at
FROM workos_reliability.incidents
WHERE occurrence_digest = sqlc.arg(occurrence_digest);

-- name: ListPendingActionIncidents :many
-- Crash recovery is deliberately not owner-scoped: this is a private
-- supervisor queue over reliability-owned rows, not an owner-facing list.
SELECT i.id, i.owner_user_id, i.project_id, i.app_instance_id, i.app_id, i.workload_id,
       i.workload_generation, i.violation, i.severity, i.summary, i.occurrence_digest,
       i.evidence_digest, i.state, i.restart_outcome, i.revision, i.acknowledge_key,
       i.acknowledged_at, i.mitigated_at, i.resolved_at, i.repair_task_id, i.created_at, i.updated_at
FROM workos_reliability.incidents AS i
LEFT JOIN workos_reliability.incident_actions AS a
  ON a.incident_id = i.id
 AND a.action = CASE WHEN i.violation = 'restart_limit_exhausted' THEN 'terminate' ELSE 'restart' END
WHERE i.state = 'open' AND i.restart_outcome = 'pending'
-- Never-attempted work runs before retries. An unavailable retry updates the
-- action timestamp and rotates behind its peers, so a bounded batch cannot
-- permanently starve a newer incident during a long Runtime outage.
ORDER BY (a.incident_id IS NOT NULL), COALESCE(a.updated_at, i.created_at),
         (i.violation = 'restart_limit_exhausted') DESC, i.created_at, i.id
LIMIT sqlc.arg(row_limit);

-- name: ListMitigatedIncidentsForWorkload :many
-- A healthy replacement generation resolves repaired incidents from the
-- generation that caused the restart, as well as any earlier generation.
SELECT id, owner_user_id, project_id, app_instance_id, app_id, workload_id,
       workload_generation, violation, severity, summary, occurrence_digest,
       evidence_digest, state, restart_outcome, revision, acknowledge_key,
       acknowledged_at, mitigated_at, resolved_at, repair_task_id, created_at, updated_at
FROM workos_reliability.incidents
WHERE workload_id = sqlc.arg(workload_id)
  AND workload_generation <= sqlc.arg(through_generation)
  AND state = 'mitigated'
ORDER BY created_at, id;

-- Notification publication facts (ADR-0014). These statements touch only
-- workos_reliability.notification_publications; Core consumes them over the
-- private source service and never issues this SQL.

-- name: InsertIncidentNotificationPublication :execrows
INSERT INTO workos_reliability.notification_publications (
    id, incident_id, owner_user_id, project_id, severity, action_outcome,
    digest, occurred_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(incident_id), sqlc.arg(owner_user_id), sqlc.arg(project_id),
    sqlc.arg(severity), sqlc.arg(action_outcome), sqlc.arg(digest),
    sqlc.arg(occurred_at), sqlc.arg(created_at)
);

-- name: ClaimPendingIncidentPublications :many
UPDATE workos_reliability.notification_publications AS pub
SET claim_locked_by = sqlc.arg(worker_id),
    claim_token = sqlc.arg(claim_token),
    claim_locked_until = sqlc.arg(lease_until),
    claim_attempts = pub.claim_attempts + 1
WHERE pub.id IN (
    SELECT pending.id FROM workos_reliability.notification_publications AS pending
    WHERE pending.outcome IS NULL
      AND (pending.claim_locked_until IS NULL OR pending.claim_locked_until < sqlc.arg(now))
    ORDER BY pending.occurred_at, pending.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(max_batch)
)
RETURNING pub.id, pub.incident_id, pub.owner_user_id, pub.project_id, pub.severity,
          pub.action_outcome, pub.digest, pub.occurred_at, pub.claim_locked_until;

-- One batch is one claim: every claimed publication shares the claim's
-- lease token, so completion proves worker + live lease for the whole batch.
-- name: CompleteIncidentPublications :execrows
UPDATE workos_reliability.notification_publications
SET outcome = 'completed',
    completed_at = sqlc.arg(now),
    completed_by = sqlc.arg(worker_id),
    claim_locked_by = NULL,
    claim_token = NULL,
    claim_locked_until = NULL
WHERE id = ANY (sqlc.arg(ids)::uuid[])
  AND claim_locked_by = sqlc.arg(worker_id)
  AND claim_token = sqlc.arg(claim_token)
  AND outcome IS NULL
  AND claim_locked_until IS NOT NULL
  AND claim_locked_until > sqlc.arg(now);

-- name: CountPendingIncidentPublications :one
SELECT count(*) FROM workos_reliability.notification_publications WHERE outcome IS NULL;

-- Repair orchestrator (ADR-0016 section 5): open incidents without a repair
-- ledger row, the per-incident ledger row, and the incident's repair-task
-- projection.

-- name: ListRepairCandidates :many
-- One repair record per incident, regardless of lifecycle state: the 1s
-- supervision cadence resolves incidents within seconds, so the lifecycle
-- window cannot be the repair trigger. The ledger row is the audit record.
SELECT i.id, i.owner_user_id, i.project_id, i.app_instance_id, i.summary
FROM workos_reliability.incidents i
LEFT JOIN workos_reliability.repair_ledger l ON l.incident_id = i.id
WHERE l.incident_id IS NULL
ORDER BY i.created_at
LIMIT $1;

-- name: InsertRepairLedger :execrows
INSERT INTO workos_reliability.repair_ledger (
    incident_id, project_id, task_id, state, attempts, created_at, updated_at
) VALUES ($1, $2, $3, 'submitted', 1, $4, $4)
ON CONFLICT (incident_id) DO NOTHING;

-- name: GetRepairLedger :one
SELECT incident_id, project_id, task_id, state, attempts, created_at, updated_at
FROM workos_reliability.repair_ledger
WHERE incident_id = $1;

-- name: UpdateIncidentRepairTask :execrows
UPDATE workos_reliability.incidents
SET repair_task_id = $1
WHERE id = $2;

-- Deployment controller (ADR-0016 section 6).

-- name: StartDeploymentLedger :execrows
INSERT INTO workos_reliability.deployment_ledger (
    incident_id, owner_user_id, project_id, installation_id, target_version,
    expected_revision, state, canary_until, canary_started_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'candidate', sqlc.arg(created_at), sqlc.arg(created_at), sqlc.arg(created_at), sqlc.arg(created_at))
ON CONFLICT (incident_id) DO UPDATE SET incident_id = EXCLUDED.incident_id
WHERE deployment_ledger.owner_user_id = EXCLUDED.owner_user_id
  AND deployment_ledger.project_id = EXCLUDED.project_id
  AND deployment_ledger.installation_id = EXCLUDED.installation_id
  AND deployment_ledger.target_version = EXCLUDED.target_version
  AND deployment_ledger.expected_revision = EXCLUDED.expected_revision;

-- name: LockPendingDeployments :many
SELECT d.*, EXISTS (
    SELECT 1 FROM workos_reliability.incidents i
    WHERE i.owner_user_id = d.owner_user_id AND i.project_id = d.project_id
      AND i.app_instance_id = d.installation_id AND i.id <> d.incident_id
      AND i.created_at >= d.canary_started_at
) AS new_incident
FROM workos_reliability.deployment_ledger d
WHERE d.state IN ('candidate', 'starting', 'canary', 'rollback')
ORDER BY d.updated_at, d.incident_id
LIMIT $1 FOR UPDATE OF d SKIP LOCKED;

-- name: SaveDeployment :exec
UPDATE workos_reliability.deployment_ledger
SET state = $2, attempts = $3, canary_started_at = $4, canary_until = $5, updated_at = $6
WHERE incident_id = $1 AND state IN ('candidate', 'starting', 'canary', 'rollback');

-- name: ListRepairCompleted :many
-- Submitted repair rows whose task terminal state is unknown to the
-- orchestrator; the orchestrator asks Core which ones completed.
SELECT l.incident_id, l.project_id, l.task_id, i.owner_user_id, i.app_instance_id, i.summary
FROM workos_reliability.repair_ledger l
JOIN workos_reliability.incidents i ON i.id = l.incident_id
WHERE l.state = 'submitted'
ORDER BY l.created_at
LIMIT $1;

-- name: ClearRepairCompleted :execrows
UPDATE workos_reliability.repair_ledger
SET state = 'terminal', updated_at = $2
WHERE incident_id = $1 AND state = 'submitted';
