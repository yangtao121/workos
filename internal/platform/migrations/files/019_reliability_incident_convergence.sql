-- 019: reliability-host incident/action convergence constraints
-- (owner: reliability-host Supervisor / Incident Manager; ADR-0006).

ALTER TABLE workos_reliability.incident_actions
    DROP CONSTRAINT incident_actions_outcome_check,
    ADD CONSTRAINT incident_actions_outcome_check CHECK (
        outcome IN ('restarted', 'stopped', 'limit_exhausted', 'conflict',
                    'unsupported', 'unavailable', 'failed')
    );
