-- 025: app installation version history (owner: workos-core Project
-- Installation, ADR-0012).
--
-- Owner-triggered version transition and previous-pinned-version rollback
-- need a durable, bounded, append-only history of the pinned identity facts
-- of each installation:
--   * project_app_installation_versions — one immutable snapshot per
--     version change (install origin, transition, rollback), keyed
--     (installation_id, sequence) with a per-installation monotonic
--     sequence; the composite foreign key binds every snapshot to the same
--     owner's installation (005 introduced UNIQUE (owner_user_id, id));
--   * project_app_installation_requests.result_version /
--     result_manifest_digest — the exact pinned identity the first response
--     carried, so transition/rollback keys replay their first result even
--     after later commands moved the installation to other versions. The
--     backfill is fail-closed exactly like 005/011: every existing mapping
--     is filled from its owner-bound installation (identity was immutable
--     until this migration, so the current row is the first-response fact),
--     and any mapping that cannot be resolved aborts the migration instead
--     of being silently dropped. Existing install/uninstall/set-grants
--     rows replay through the same snapshot columns after the backfill;
--   * the command list accepts 'transition' and 'rollback', sharing the
--     existing (owner_user_id, idempotency_key) namespace.
--
-- Bounded history: application writes keep at most the 20 most recent
-- snapshots per installation (application-enforced trim in the same
-- transaction; the sequence column makes the trim deterministic).

CREATE TABLE workos_core.project_app_installation_versions (
    installation_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence >= 1),
    version text NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    source text NOT NULL CHECK (source IN ('install', 'transition', 'rollback')),
    occurred_at timestamptz NOT NULL,
    PRIMARY KEY (installation_id, sequence),
    CONSTRAINT project_app_installation_versions_installation_fkey
        FOREIGN KEY (installation_id, owner_user_id)
        REFERENCES workos_core.project_app_installations (id, owner_user_id)
        ON DELETE CASCADE
);

CREATE INDEX project_app_installation_versions_owner_idx
    ON workos_core.project_app_installation_versions (owner_user_id, installation_id, sequence);

ALTER TABLE workos_core.project_app_installation_requests
    DROP CONSTRAINT project_app_installation_requests_command_check;

ALTER TABLE workos_core.project_app_installation_requests
    ADD CONSTRAINT project_app_installation_requests_command_check
    CHECK (command IN ('install', 'uninstall', 'set-grants', 'transition', 'rollback'));

ALTER TABLE workos_core.project_app_installation_requests
    ADD COLUMN result_version text,
    ADD COLUMN result_manifest_digest text;

-- Fail-closed backfill: identity was immutable before this migration, so
-- the owner-bound installation's current pinned facts are every existing
-- mapping's first-response snapshot.
UPDATE workos_core.project_app_installation_requests r
SET result_version = i.version,
    result_manifest_digest = i.manifest_digest
FROM workos_core.project_app_installations i
WHERE r.installation_id = i.id
  AND r.owner_user_id = i.owner_user_id;

DO $$
DECLARE
    unbackfilled integer;
BEGIN
    SELECT count(*) INTO unbackfilled
    FROM workos_core.project_app_installation_requests r
    WHERE r.result_version IS NULL OR r.result_manifest_digest IS NULL;
    IF unbackfilled > 0 THEN
        RAISE EXCEPTION 'project_app_installation_requests has % mappings without an owner-bound version snapshot', unbackfilled;
    END IF;
END $$;

ALTER TABLE workos_core.project_app_installation_requests
    ALTER COLUMN result_version SET NOT NULL,
    ALTER COLUMN result_manifest_digest SET NOT NULL;

-- Seed the history origin: every existing installation gets its install
-- snapshot so the first transition has a real previous state to roll back
-- to.
INSERT INTO workos_core.project_app_installation_versions (
    installation_id, owner_user_id, sequence, version, manifest_digest, source, occurred_at
)
SELECT id, owner_user_id, 1, version, manifest_digest, 'install', installed_at
FROM workos_core.project_app_installations;
