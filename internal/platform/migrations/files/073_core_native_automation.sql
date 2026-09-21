-- Owner: core Agent. Canonical projections and human commands; native goal
-- scheduling and context continue to belong to the harness session log.
ALTER TABLE workos_core.agent_sessions
 ADD COLUMN goal_projection jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(goal_projection) = 'object'),
 ADD COLUMN goal_pause_ref text NOT NULL DEFAULT '' CHECK (length(goal_pause_ref) <= 128);

ALTER TABLE workos_core.agent_session_inputs
 ADD COLUMN directive jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(directive) = 'object');
ALTER TABLE workos_core.agent_session_inputs DROP CONSTRAINT agent_session_inputs_input_text_check;
ALTER TABLE workos_core.agent_session_inputs ADD CONSTRAINT agent_session_inputs_input_text_check
 CHECK ((directive = '{}' AND length(input_text) BETWEEN 1 AND 65536)
     OR (directive <> '{}' AND input_text = ''));

CREATE TABLE workos_core.agent_goal_pause_requests (
 session_id uuid NOT NULL REFERENCES workos_core.agent_sessions(session_id),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 goal_ref text NOT NULL CHECK (length(goal_ref) BETWEEN 1 AND 128),
 created_at timestamptz NOT NULL,
 PRIMARY KEY (session_id, idempotency_key)
);

CREATE TABLE workos_core.agent_delegations (
 id uuid PRIMARY KEY CHECK (id::text ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
 owner_user_id uuid NOT NULL,
 session_id uuid NOT NULL REFERENCES workos_core.agent_sessions(session_id),
 task_id uuid NOT NULL REFERENCES workos_core.agent_tasks(id),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 title text NOT NULL CHECK (length(title) BETWEEN 1 AND 256),
 binding_id uuid NOT NULL,
 binding_revision bigint NOT NULL CHECK (binding_revision > 0),
 source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 256),
 state text NOT NULL CHECK (state IN ('preparing','running','completed','failed','cancelled','needs_review')),
 worktree_id text NOT NULL DEFAULT '' CHECK (length(worktree_id) <= 128),
 base_commit text NOT NULL DEFAULT '' CHECK (length(base_commit) <= 64),
 result_summary text NOT NULL DEFAULT '' CHECK (length(result_summary) <= 2048),
 result_artifact_id text NOT NULL DEFAULT '' CHECK (length(result_artifact_id) <= 128),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE (task_id,idempotency_key)
);
CREATE INDEX agent_delegations_session_idx ON workos_core.agent_delegations(session_id,created_at DESC,id);
