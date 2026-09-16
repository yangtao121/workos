-- 067_core_version_artifacts.sql
-- Owner: core app registry (ADR-0033). Auditable, indexed binding from every
-- version row to the exact release bundle its canonical manifest carries.
-- The canonical manifest remains the source of truth; these columns exist so
-- provenance queries never need to parse manifests. Legacy rows stay NULL
-- (image-only profile) and are never backfilled or guessed.
ALTER TABLE workos_core.app_versions
    ADD COLUMN artifact_id uuid,
    ADD COLUMN artifact_digest text
        CHECK (artifact_digest IS NULL OR artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN artifact_format text
        CHECK (artifact_format IS NULL OR artifact_format IN ('app-bundle.v1')),
    ADD CONSTRAINT app_versions_artifact_all_or_none_check
        CHECK ((artifact_id IS NULL AND artifact_digest IS NULL AND artifact_format IS NULL)
            OR (artifact_id IS NOT NULL AND artifact_digest IS NOT NULL AND artifact_format IS NOT NULL));

CREATE INDEX app_versions_artifact_idx
    ON workos_core.app_versions (owner_user_id, artifact_digest)
    WHERE artifact_digest IS NOT NULL;
