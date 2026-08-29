-- 011: mutable project app grants (owner: workos-core Project Installation).
--
-- ADR-0003 makes the installation grant set mutable through the explicit
-- full-replacement SetAppGrants command. This migration adds the durable
-- facts that change requires, all inside workos-core Project Installation
-- tables:
--   * project_app_installations.grant_revision — the authorization epoch of
--     one installation: 1 from install on, +1 only when the grant set
--     actually changes. Existing rows backfill to 1 because no mutation path
--     existed before this migration;
--   * the request mapping's command list accepts 'set-grants', sharing the
--     existing (owner_user_id, idempotency_key) namespace with install and
--     uninstall;
--   * result_granted_permissions / result_grant_revision persist the precise
--     first-response snapshot. Once grants are mutable, replaying a
--     historical install/uninstall key must return the grant and revision of
--     the first response, never the installation's later mutated row, so the
--     mapping stops being derivable from the live installation and becomes a
--     fact of its own.
--
-- The backfill is fail-closed exactly like 005: every existing mapping is
-- filled from its owner-bound installation (grants never changed before this
-- migration, so the installation's current grant and revision 1 are the
-- first-response facts), and any mapping that cannot be resolved stops the
-- migration with a countable violation instead of being silently dropped or
-- rewritten. The runner executes this file in a single transaction, so a
-- raised exception leaves the schema and data untouched.

ALTER TABLE workos_core.project_app_installations
    ADD COLUMN grant_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT project_app_installations_grant_revision_positive
        CHECK (grant_revision >= 1);

ALTER TABLE workos_core.project_app_installation_requests
    DROP CONSTRAINT project_app_installation_requests_command_check;

ALTER TABLE workos_core.project_app_installation_requests
    ADD CONSTRAINT project_app_installation_requests_command_check
    CHECK (command IN ('install', 'uninstall', 'set-grants'));

ALTER TABLE workos_core.project_app_installation_requests
    ADD COLUMN result_granted_permissions text[] NOT NULL DEFAULT '{}',
    ADD COLUMN result_grant_revision bigint;

-- Backfill each mapping's result snapshot from its owner-bound installation.
-- The 005 composite foreign key already guarantees the owner binding; the
-- revision is 1 for every row because grant mutation did not exist yet.
UPDATE workos_core.project_app_installation_requests r
SET result_granted_permissions = i.granted_permissions,
    result_grant_revision = 1
FROM workos_core.project_app_installations i
WHERE r.installation_id = i.id
  AND r.owner_user_id = i.owner_user_id;

-- Fail closed before the column becomes NOT NULL: any mapping that was not
-- backfilled (missing or owner-inconsistent installation) is a durable
-- idempotency result whose first response can no longer be reconstructed —
-- report it, change nothing.
DO $$
DECLARE
    unbackfilled integer;
BEGIN
    SELECT count(*) INTO unbackfilled
    FROM workos_core.project_app_installation_requests r
    WHERE r.result_grant_revision IS NULL;
    IF unbackfilled > 0 THEN
        RAISE EXCEPTION 'project_app_installation_requests has % result mappings without an owner-bound installation snapshot', unbackfilled;
    END IF;
END $$;

ALTER TABLE workos_core.project_app_installation_requests
    ALTER COLUMN result_grant_revision SET NOT NULL;
