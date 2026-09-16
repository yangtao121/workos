-- name: GetPreviewByOwnerKey :one
SELECT * FROM workos_runtime.workspace_previews WHERE owner_user_id=$1 AND idempotency_key=$2;

-- name: InsertWorkspacePreview :execrows
INSERT INTO workos_runtime.workspace_previews(preview_id,owner_user_id,project_id,idempotency_key,workspace_source_id,read_only,state,created_at,updated_at,expires_at,command,port,generation,access_token,request_digest,binding_id,binding_revision)
VALUES(sqlc.arg(preview_id),sqlc.arg(owner_user_id),sqlc.arg(project_id),sqlc.arg(idempotency_key),sqlc.arg(workspace_source_id),sqlc.arg(read_only),sqlc.arg(state),sqlc.arg(created_at),sqlc.arg(updated_at),sqlc.arg(expires_at),sqlc.arg(command),sqlc.arg(port),sqlc.arg(generation),sqlc.arg(access_token),sqlc.arg(request_digest),sqlc.arg(binding_id),sqlc.arg(binding_revision)) ON CONFLICT DO NOTHING;

-- name: GetWorkspacePreview :one
SELECT * FROM workos_runtime.workspace_previews WHERE preview_id=$1;

-- name: ListProjectWorkspacePreviews :many
SELECT * FROM workos_runtime.workspace_previews WHERE owner_user_id=$1 AND project_id=$2 ORDER BY created_at DESC,preview_id DESC LIMIT $3;

-- name: UpdateWorkspacePreviewState :execrows
UPDATE workos_runtime.workspace_previews SET state=$3,updated_at=$4 WHERE owner_user_id=$1 AND preview_id=$2;

-- name: ListActiveWorkspacePreviews :many
SELECT * FROM workos_runtime.workspace_previews WHERE state IN ('queued','running');

-- name: LockWorkspacePreview :one
SELECT * FROM workos_runtime.workspace_previews WHERE owner_user_id=$1 AND preview_id=$2 FOR UPDATE;

-- name: GetWorkspacePreviewAction :one
SELECT action FROM workos_runtime.workspace_preview_actions WHERE preview_id=$1 AND action_key=$2;

-- name: RecordWorkspacePreviewAction :exec
INSERT INTO workos_runtime.workspace_preview_actions(preview_id,action_key,action,generation) VALUES($1,$2,$3,$4);

-- name: RestartWorkspacePreview :one
UPDATE workos_runtime.workspace_previews SET generation=generation+1,state='queued',updated_at=$3,expires_at=$4 WHERE owner_user_id=$1 AND preview_id=$2 RETURNING *;

-- name: ActivateWorkspacePreview :execrows
UPDATE workos_runtime.workspace_previews SET state='running',workspace_source_id=$3,read_only=$4,binding_id=$5,binding_revision=$6,updated_at=$7 WHERE owner_user_id=$1 AND preview_id=$2 AND state='queued';
