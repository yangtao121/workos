package application

import (
	"context"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

func (s *Service) WithConnectivity(issuer ports.ConnectivityIssuer) *Service {
	s.connectivity = issuer
	return s
}

func (s *Service) Connectivity(ctx context.Context, owner, device, sessionID string, epoch int64) (ports.Connectivity, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(owner) || !domain.ValidUUIDv7(device) || !domain.ValidUUIDv7(sessionID) {
		return ports.Connectivity{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, owner, sessionID)
	if err != nil {
		return ports.Connectivity{}, err
	}
	session, err = s.reconcile(ctx, session)
	if err != nil {
		return ports.Connectivity{}, err
	}
	if session.State != domain.StateRunning {
		return ports.Connectivity{}, domain.ErrEngineUnavailable
	}
	if _, err := s.workspaceGrant(ctx, owner, session.ProjectID); err != nil {
		return ports.Connectivity{}, err
	}
	gate, ok := s.control.(ports.EpochControlAuthorizer)
	if !ok || gate.AuthorizeInputGeneration(ctx, owner, sessionID, device, epoch) != nil {
		return ports.Connectivity{}, domain.ErrControlDenied
	}
	if s.connectivity == nil {
		return ports.Connectivity{}, domain.ErrEngineUnavailable
	}
	return s.connectivity.Issue()
}
