-- Owner: core.agent. Questions belong to one live execution lease.
CREATE TABLE workos_core.agent_execution_interactions (
 id uuid PRIMARY KEY,
 task_id uuid NOT NULL REFERENCES workos_core.agent_tasks(id),
 owner_user_id uuid NOT NULL,
 project_id uuid NOT NULL,
 lease_id uuid NOT NULL,
 worker_id text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 questions jsonb NOT NULL,
 answers jsonb NOT NULL DEFAULT '[]',
 state text NOT NULL CHECK(state IN ('pending','answered','rejected','expired')),
 decision_key text NOT NULL DEFAULT '',
 decision_digest text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 UNIQUE(task_id,request_key)
);
CREATE INDEX agent_interactions_task ON workos_core.agent_execution_interactions(owner_user_id,task_id,created_at);
