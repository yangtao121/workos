-- 008: installation-level explicit grant snapshot (owner: workos-core Project Installation).
--
-- Manifest permissions remain requested permissions forever; this column adds
-- the separate durable grant fact the user explicitly approved at install
-- time. It is the only grant authority in the system:
--   * canonical sorted, duplicate-free subset of the pinned version's
--     requested permissions — canonicality and the subset rule are enforced
--     inside the install transaction by the owning application service
--     (PostgreSQL CHECK constraints cannot express subqueries over arrays);
--   * empty grant is valid and is the default: every installation that
--     predates this migration backfills to '{}' and NEVER receives its
--     historical requested permissions as a grant;
--   * immutable for the installation's lifetime — changing the grant requires
--     uninstall + reinstall, there is no mutable update path in this slice.

ALTER TABLE workos_core.project_app_installations
    ADD COLUMN granted_permissions text[] NOT NULL DEFAULT '{}';
