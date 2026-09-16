-- name: BeginWorkspaceOperation :execrows
INSERT INTO workos_runtime.workspace_operations(operation_id,owner_user_id,project_id,request_digest,state)
VALUES($1,$2,$3,$4,'pending') ON CONFLICT(operation_id) DO NOTHING;

-- name: GetWorkspaceOperation :one
SELECT owner_user_id,project_id,request_digest,state,result FROM workos_runtime.workspace_operations WHERE operation_id=$1;

-- name: CompleteWorkspaceOperation :execrows
UPDATE workos_runtime.workspace_operations SET state='completed',result=$4,completed_at=now()
WHERE operation_id=$1 AND owner_user_id=$2 AND request_digest=$3 AND state='pending';
