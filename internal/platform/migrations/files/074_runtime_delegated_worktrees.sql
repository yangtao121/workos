-- Owner: runtime WorkspaceHost. Private Git metadata and working trees are
-- selected by these immutable scope facts, never by model-supplied paths.
CREATE TABLE workos_runtime.delegated_worktrees (
 delegation_id uuid PRIMARY KEY,
 parent_task_id uuid NOT NULL,
 owner_user_id uuid NOT NULL,
 project_id uuid NOT NULL,
 binding_id uuid NOT NULL,
 binding_revision bigint NOT NULL CHECK (binding_revision > 0),
 source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 256),
 state text NOT NULL CHECK (state IN ('preparing','ready','needs_review')),
 base_commit text NOT NULL DEFAULT '' CHECK (base_commit = '' OR base_commit ~ '^[0-9a-f]{40,64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
