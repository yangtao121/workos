package coreclient

import (
	"connectrpc.com/connect"
	"context"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
	"time"
)

type Authorization struct {
	Client projectv1connect.WorkspaceExecutionAuthorizationServiceClient
}

func (a Authorization) Resolve(ctx context.Context, owner, project string) (ports.Authorization, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := a.Client.ResolveWorkspaceExecution(ctx, connect.NewRequest(&projectv1.ResolveWorkspaceExecutionRequest{OwnerUserId: owner, ProjectId: project}))
	if err != nil {
		return ports.Authorization{}, domain.ErrDenied
	}
	b := response.Msg.GetBinding()
	if b.GetOwnerUserId() != owner || b.GetProjectId() != project || b.GetState() != projectv1.WorkspaceBindingState_WORKSPACE_BINDING_STATE_ACTIVE || b.GetRevision() < 1 {
		return ports.Authorization{}, domain.ErrDenied
	}
	return ports.Authorization{BindingID: b.GetId(), SourceID: b.GetWorkspaceSourceId(), Revision: b.GetRevision(), ReadOnly: b.GetReadOnly()}, nil
}
