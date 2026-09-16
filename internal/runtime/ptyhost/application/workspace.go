package application

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	"time"
)

func (s *Service) WithWorkspaceAuthorization(a ports.WorkspaceAuthorizer) *Service {
	s.authorization = a
	return s
}
func (s *Service) workspaceGrant(ctx context.Context, owner, project string) (ports.WorkspaceGrant, error) {
	if s.authorization != nil {
		return s.authorization.AuthorizeWorkspace(ctx, owner, project)
	}
	grant := ports.WorkspaceGrant{}
	if s.workspace != nil {
		grant.Directory, _ = s.workspace.WorkingDirectory(owner, project)
	}
	return grant, nil
}
func (s *Service) launch(ctx context.Context, a, b int32, g ports.WorkspaceGrant) (ports.Terminal, error) {
	if engine, ok := s.engine.(ports.WorkspaceEngine); ok {
		return engine.LaunchWorkspace(ctx, a, b, g.Directory, g.ReadOnly)
	}
	if g.ReadOnly || g.Validate != nil {
		return nil, domain.ErrEngineUnavailable
	}
	return s.engine.Launch(ctx, a, b, g.Directory)
}

// Each watcher belongs to one generation. A late revocation cannot stop its
// replacement. Core outage fails closed just like a revoked binding.
func (s *Service) watchWorkspace(session domain.Session, g ports.WorkspaceGrant) {
	if g.Validate == nil {
		return
	}
	go func() {
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for range timer.C {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := g.Validate(ctx)
			cancel()
			s.opMu.Lock()
			current, lookup := s.store.GetSession(context.Background(), session.OwnerUserID, session.SessionID)
			if lookup != nil || current.Generation != session.Generation || current.State.Terminal() {
				s.opMu.Unlock()
				return
			}
			if err != nil {
				s.reap(session.SessionID)
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.store.CloseSession(cleanup, session.OwnerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
				done()
				s.opMu.Unlock()
				return
			}
			s.opMu.Unlock()
		}
	}()
}
