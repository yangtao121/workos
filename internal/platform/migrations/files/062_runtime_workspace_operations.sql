-- Owner: runtime-host workspace execution. Never replay an uncertain mutation.
CREATE TABLE workos_runtime.workspace_operations (
 operation_id uuid PRIMARY KEY,
 owner_user_id uuid NOT NULL,
 project_id uuid NOT NULL,
 request_digest text NOT NULL,
 state text NOT NULL CHECK (state IN ('pending','completed')),
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz
);
