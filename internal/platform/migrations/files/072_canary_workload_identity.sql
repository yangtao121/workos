-- Owner: reliability-host. Freeze the running generation before starting canary.
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN workload_id uuid,
    ADD COLUMN workload_generation bigint,
    ADD CONSTRAINT deployment_workload_identity_pair CHECK (
        (workload_id IS NULL AND workload_generation IS NULL) OR
        (workload_id IS NOT NULL AND workload_generation IS NOT NULL AND workload_generation > 0)
    );
