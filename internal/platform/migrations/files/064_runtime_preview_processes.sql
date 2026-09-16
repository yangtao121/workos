-- Owner: runtime-host previewhost. Process facts and durable action receipts.
ALTER TABLE workos_runtime.workspace_previews
 ADD COLUMN command text NOT NULL DEFAULT '' CHECK(length(command)<=4096),
 ADD COLUMN port integer NOT NULL DEFAULT 3000 CHECK(port BETWEEN 1024 AND 65535),
 ADD COLUMN generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 ADD COLUMN access_token text NOT NULL DEFAULT '',
 ADD COLUMN request_digest text NOT NULL DEFAULT '',
 ADD COLUMN binding_id text NOT NULL DEFAULT '',
 ADD COLUMN binding_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE workos_runtime.workspace_previews DROP CONSTRAINT workspace_previews_state_check;
ALTER TABLE workos_runtime.workspace_previews ADD CONSTRAINT workspace_previews_state_check CHECK(state IN ('queued','running','stopped','expired','failed'));
DROP INDEX workos_runtime.workspace_previews_owner_key_live_unique;
CREATE UNIQUE INDEX workspace_previews_owner_key_unique ON workos_runtime.workspace_previews(owner_user_id,idempotency_key);
CREATE TABLE workos_runtime.workspace_preview_actions (
 preview_id uuid NOT NULL REFERENCES workos_runtime.workspace_previews(preview_id),
 action_key text NOT NULL CHECK(length(action_key) BETWEEN 1 AND 128),
 action text NOT NULL CHECK(action IN ('stop','restart')),
 generation bigint NOT NULL,
 PRIMARY KEY(preview_id,action_key)
);
