-- 004: project app installations (owner: workos-core Project Installation).
-- The installation table is the authoritative fact for app instances in a
-- project; the projects.installed_app_ids array stays as a transactionally
-- maintained derived projection written by the same Project repository.

-- Enables the composite foreign key binding every installation to the
-- project's owner at the database level.
ALTER TABLE workos_core.projects
    ADD CONSTRAINT projects_id_owner_unique UNIQUE (id, owner_user_id);

CREATE TABLE workos_core.project_app_installations (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    project_id uuid NOT NULL,
    app_id text NOT NULL CHECK (app_id ~ '^[a-z][a-z0-9-]{2,62}$'),
    version text NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    installed_at timestamptz NOT NULL,
    uninstalled_at timestamptz,
    CONSTRAINT project_app_installations_project_owner_fkey
        FOREIGN KEY (project_id, owner_user_id)
        REFERENCES workos_core.projects (id, owner_user_id)
        ON DELETE RESTRICT,
    CONSTRAINT project_app_installations_coherent_times CHECK (
        uninstalled_at IS NULL OR uninstalled_at >= installed_at
    )
);

-- One active installation per (project, app); tombstoned history is retained.
CREATE UNIQUE INDEX project_app_installations_active_idx
    ON workos_core.project_app_installations (project_id, app_id)
    WHERE uninstalled_at IS NULL;

CREATE INDEX project_app_installations_owner_idx
    ON workos_core.project_app_installations (owner_user_id, id);

-- Authoritative install/uninstall command idempotency: one persisted result
-- per (owner, idempotency key). The request digest covers the canonical
-- client request only (command, project, app, requested version, expected
-- revision, installation id) so replays never re-resolve registry currents.
CREATE TABLE workos_core.project_app_installation_requests (
    owner_user_id uuid NOT NULL REFERENCES workos_core.users (id),
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    command text NOT NULL CHECK (command IN ('install', 'uninstall')),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    installation_id uuid NOT NULL,
    project_revision bigint NOT NULL CHECK (project_revision > 0),
    result_uninstalled_at timestamptz,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (owner_user_id, idempotency_key),
    CONSTRAINT project_app_installation_requests_installation_fkey
        FOREIGN KEY (installation_id)
        REFERENCES workos_core.project_app_installations (id)
        ON DELETE RESTRICT
);
