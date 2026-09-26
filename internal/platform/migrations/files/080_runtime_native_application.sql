-- Runtime owns Native session application identity. Existing rows are Code.
ALTER TABLE workos_runtime.native_sessions
  ADD COLUMN app_kind text NOT NULL DEFAULT 'code'
  CONSTRAINT native_sessions_app_kind_check CHECK (app_kind IN ('code', 'text_editor'));
