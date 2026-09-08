-- Owner: reliability-host (ADR-0016 §5 / ADR-0026 Recovery governance).
-- A repair whose project harness AND the configured recovery harness are
-- both unavailable terminates in awaiting_manual: the incident's existing
-- notification chain informs the owner, and the orchestrator stops
-- retrying the same admission.
ALTER TABLE workos_reliability.repair_ledger
    DROP CONSTRAINT repair_ledger_state_check,
    ADD CONSTRAINT repair_ledger_state_check CHECK (state IN ('submitted', 'terminal', 'awaiting_manual'));
