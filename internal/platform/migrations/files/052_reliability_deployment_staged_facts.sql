-- Owner: reliability-host (ADR-0026). The deployment ledger carries the
-- staged candidate facts so a restart preserves the verified-chain identity:
-- the producing task, the staged manifest digest and the version the
-- installation pinned when the staged version was registered.
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN task_id uuid;
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN manifest_digest text
    CHECK (manifest_digest IS NULL OR manifest_digest ~ '^sha256:[0-9a-f]{64}$');
ALTER TABLE workos_reliability.deployment_ledger
    ADD COLUMN base_version text;
