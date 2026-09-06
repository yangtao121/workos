package coreclient

import (
	"connectrpc.com/connect"
	"context"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	artifactv1connect "github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

type AppArtifacts struct {
	Client artifactv1connect.AppArtifactServiceClient
}

func artifactScope(q ports.AppArtifactQuery) *artifactv1.AppArtifactScope {
	return &artifactv1.AppArtifactScope{ProjectId: q.ProjectID, AppInstanceId: q.AppInstanceID, InstallationGrantRevision: q.GrantRevision}
}
func (a AppArtifacts) Create(ctx context.Context, q ports.AppArtifactQuery, input ports.AppArtifactInput) (*artifactv1.Artifact, error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	req := connect.NewRequest(&artifactv1.CreateAppReviewArtifactRequest{Scope: artifactScope(q), IdempotencyKey: input.Key, Type: input.Type, Title: input.Title, Content: input.Content})
	req.Header().Set(identity.UserHeader, id.UserID)
	req.Header().Set(identity.DeviceHeader, id.DeviceID)
	response, err := a.Client.CreateAppReviewArtifact(ctx, req)
	if err != nil {
		return nil, artifactError(err)
	}
	return response.Msg.GetArtifact(), nil
}
func (a AppArtifacts) Open(ctx context.Context, q ports.AppArtifactQuery, artifactID string) (*artifactv1.Artifact, error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	req := connect.NewRequest(&artifactv1.OpenAppReviewArtifactRequest{Scope: artifactScope(q), ArtifactId: artifactID})
	req.Header().Set(identity.UserHeader, id.UserID)
	req.Header().Set(identity.DeviceHeader, id.DeviceID)
	response, err := a.Client.OpenAppReviewArtifact(ctx, req)
	if err != nil {
		return nil, artifactError(err)
	}
	return response.Msg.GetArtifact(), nil
}

func artifactError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return domain.ErrInvalid
	case connect.CodeNotFound:
		return domain.ErrNotFound
	default:
		return mapAppAgentError(err)
	}
}
