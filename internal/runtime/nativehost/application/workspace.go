package application

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
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
func (s *Service) launch(ctx context.Context, sessionID string, a, b int32, g ports.WorkspaceGrant, mode domain.LifecycleMode) (ports.Display, error) {
	if _, ok := s.engine.(ports.ResidentEngine); ok {
		return nil, domain.ErrEngineUnavailable
	}
	if engine, ok := s.engine.(interface {
		LaunchSessionLifecycle(context.Context, string, int32, int32, string, bool, domain.LifecycleMode) (ports.Display, error)
	}); ok {
		return engine.LaunchSessionLifecycle(ctx, sessionID, a, b, g.Directory, g.ReadOnly, mode)
	}
	if engine, ok := s.engine.(ports.LifecycleEngine); ok {
		return engine.LaunchLifecycle(ctx, a, b, g.Directory, g.ReadOnly, mode)
	}
	if mode == domain.LifecycleManualStop {
		return nil, domain.ErrEngineUnavailable
	}
	if engine, ok := s.engine.(ports.WorkspaceEngine); ok {
		return engine.LaunchWorkspace(ctx, a, b, g.Directory, g.ReadOnly)
	}
	if g.ReadOnly || g.Validate != nil {
		return nil, domain.ErrEngineUnavailable
	}
	return s.engine.Launch(ctx, a, b, g.Directory)
}

func (s *Service) launchResident(ctx context.Context, session domain.Session, grant ports.WorkspaceGrant) (ports.Display, error) {
	engine, ok := s.engine.(ports.ResidentEngine)
	if !ok {
		return s.launch(ctx, session.SessionID, session.Width, session.Height, grant, session.LifecycleMode)
	}
	return engine.LaunchResident(ctx, ports.ResidentLaunch{Session: session, Workspace: grant})
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
