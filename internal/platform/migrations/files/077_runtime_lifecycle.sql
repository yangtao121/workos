-- Owner: runtime-host. ADR-0037 explicit program lifetime; old rows remain bounded.
ALTER TABLE workos_runtime.pty_sessions ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.pty_sessions ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE workos_runtime.pty_sessions ADD CONSTRAINT pty_sessions_lifecycle_expiry CHECK ((lifecycle_mode=1 AND expires_at IS NOT NULL) OR (lifecycle_mode=2 AND expires_at IS NULL));
ALTER TABLE workos_runtime.native_sessions ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.native_sessions ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE workos_runtime.native_sessions ADD CONSTRAINT native_sessions_lifecycle_expiry CHECK ((lifecycle_mode=1 AND expires_at IS NOT NULL) OR (lifecycle_mode=2 AND expires_at IS NULL));
ALTER TABLE workos_runtime.workspace_previews ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.workspace_previews ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE workos_runtime.workspace_previews ADD CONSTRAINT workspace_previews_lifecycle_expiry CHECK ((lifecycle_mode=1 AND expires_at IS NOT NULL) OR (lifecycle_mode=2 AND expires_at IS NULL));
ALTER TABLE workos_runtime.pty_session_restarts ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.native_session_restarts ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.workspace_preview_actions ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.workloads ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
ALTER TABLE workos_runtime.surface_sessions ADD COLUMN lifecycle_mode smallint NOT NULL DEFAULT 1 CHECK (lifecycle_mode IN (1,2));
