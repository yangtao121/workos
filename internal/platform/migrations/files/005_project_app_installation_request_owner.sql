-- 005: bind the authoritative installation request mapping to the referenced
-- installation's owner (owner: workos-core Project Installation).
--
-- 004's single-column foreign key allowed request(owner B) to reference
-- installation(owner A). Such a mapping is consumed for owner B's key while
-- the owner-scoped replay read can never resolve it — a durable idempotency
-- result that is permanently unreplayable. This forward migration enforces
-- request.owner_user_id == installation.owner_user_id with a composite
-- foreign key, the same owner-binding pattern App Registry 003 uses.

-- Fail closed before touching the schema: existing cross-owner mappings must
-- stop the migration with a countable violation instead of surfacing as an
-- opaque constraint error halfway through.
DO $$
DECLARE
    mismatches integer;
BEGIN
    SELECT count(*) INTO mismatches
    FROM workos_core.project_app_installation_requests r
    JOIN workos_core.project_app_installations i ON r.installation_id = i.id
    WHERE r.owner_user_id <> i.owner_user_id;
    IF mismatches > 0 THEN
        RAISE EXCEPTION 'project_app_installation_requests has % cross-owner result mappings', mismatches;
    END IF;
END $$;

-- The composite key the request mapping references.
ALTER TABLE workos_core.project_app_installations
    ADD CONSTRAINT project_app_installations_owner_id_unique UNIQUE (owner_user_id, id);

-- Replace the owner-agnostic single-column FK with the owner-bound one.
ALTER TABLE workos_core.project_app_installation_requests
    DROP CONSTRAINT project_app_installation_requests_installation_fkey;

ALTER TABLE workos_core.project_app_installation_requests
    ADD CONSTRAINT project_app_installation_requests_owner_installation_fkey
        FOREIGN KEY (owner_user_id, installation_id)
        REFERENCES workos_core.project_app_installations (owner_user_id, id)
        ON DELETE RESTRICT;

-- The 004 non-unique (owner_user_id, id) index serves the same owner-scoped
-- id lookups as the new unique constraint and is now fully redundant.
DROP INDEX workos_core.project_app_installations_owner_idx;
