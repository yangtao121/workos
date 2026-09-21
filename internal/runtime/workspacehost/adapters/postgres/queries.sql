-- name: BeginWorkspaceOperation :execrows
INSERT INTO workos_runtime.workspace_operations(operation_id,owner_user_id,project_id,request_digest,state)
VALUES($1,$2,$3,$4,'pending') ON CONFLICT(operation_id) DO NOTHING;

-- name: GetWorkspaceOperation :one
SELECT owner_user_id,project_id,request_digest,state,result FROM workos_runtime.workspace_operations WHERE operation_id=$1;

-- name: CompleteWorkspaceOperation :execrows
UPDATE workos_runtime.workspace_operations SET state='completed',result=$4,completed_at=now()
WHERE operation_id=$1 AND owner_user_id=$2 AND request_digest=$3 AND state='pending';

-- name: BeginDelegatedWorktree :execrows
INSERT INTO workos_runtime.delegated_worktrees(delegation_id,parent_task_id,owner_user_id,project_id,binding_id,binding_revision,source_id,state)
VALUES($1,$2,$3,$4,$5,$6,$7,'preparing') ON CONFLICT(delegation_id) DO NOTHING;

-- name: GetDelegatedWorktree :one
SELECT delegation_id,parent_task_id,owner_user_id,project_id,binding_id,binding_revision,source_id,state,base_commit,created_at,updated_at
FROM workos_runtime.delegated_worktrees WHERE delegation_id=$1;

-- name: CompleteDelegatedWorktree :execrows
UPDATE workos_runtime.delegated_worktrees SET state='ready',base_commit=$2,updated_at=now()
WHERE delegation_id=$1 AND state='preparing';

-- name: ReviewDelegatedWorktree :exec
UPDATE workos_runtime.delegated_worktrees SET state='needs_review',updated_at=now()
WHERE delegation_id=$1 AND state='preparing';
