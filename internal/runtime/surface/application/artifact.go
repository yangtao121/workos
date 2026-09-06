package application

import (
	"context"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func (s *BridgeService) WithArtifacts(client ports.AppArtifactClient) *BridgeService {
	s.artifacts = client
	return s
}
func (s *BridgeService) artifactScope(ctx context.Context, owner, device, token, method string) (ports.AppArtifactQuery, error) {
	session, err := s.authorizeCurrent(ctx, owner, device, token, method)
	if err != nil {
		return ports.AppArtifactQuery{}, err
	}
	if s.artifacts == nil {
		return ports.AppArtifactQuery{}, domain.ErrUnavailable
	}
	return ports.AppArtifactQuery{ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID, GrantRevision: session.InstallationGrantRevision}, nil
}
func (s *BridgeService) CreateReviewArtifact(ctx context.Context, owner, device, token string, input ports.AppArtifactInput) (*artifactv1.Artifact, error) {
	if len(input.Content) > domain.MaxFileBytes {
		return nil, domain.ErrFileLimit
	}
	scope, err := s.artifactScope(ctx, owner, device, token, "artifacts.create")
	if err != nil {
		return nil, err
	}
	a, err := s.artifacts.Create(ctx, scope, input)
	return a, err
}
func (s *BridgeService) OpenReviewArtifact(ctx context.Context, owner, device, token, id string) (*artifactv1.Artifact, error) {
	if !domain.ValidSessionUUID(id) {
		return nil, domain.ErrInvalid
	}
	scope, err := s.artifactScope(ctx, owner, device, token, "artifacts.open")
	if err != nil {
		return nil, err
	}
	a, err := s.artifacts.Open(ctx, scope, id)
	return a, err
}
