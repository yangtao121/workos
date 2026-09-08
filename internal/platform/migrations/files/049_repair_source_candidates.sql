-- Owner: Core App Registry. Source proposals are not installable App versions.
ALTER TABLE workos_core.app_source_bundles ADD UNIQUE (owner_user_id, id);
CREATE TABLE workos_core.app_repair_source_candidates (
    task_id uuid PRIMARY KEY CHECK (task_id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    owner_user_id uuid NOT NULL,
    source_bundle_id uuid NOT NULL UNIQUE,
    FOREIGN KEY (owner_user_id, source_bundle_id)
        REFERENCES workos_core.app_source_bundles (owner_user_id, id)
);
