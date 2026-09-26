package application

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

// restartResident serializes the last old-generation event ACK with child
// removal and the durable generation transition. The previous child must be
// gone before BeginRestart can make a new generation visible.
func (s *Service) restartResident(ctx context.Context, owner, id, key string, fence func() error, session domain.Session, mode domain.LifecycleMode, restarts ports.RestartStore) (domain.Session, error) {
	store, ok := s.store.(ports.ResidentStore)
	if !ok {
		return domain.Session{}, domain.ErrStoreUnavailable
	}
	if _, found, err := store.RestartReceipt(ctx, owner, id, key); err != nil {
		return domain.Session{}, err
	} else if found {
		// The repository verifies the original lifecycle mode in this path.
		if _, fresh, err := restarts.BeginRestart(ctx, owner, id, key, time.Now().UTC(), mode); err != nil {
			return domain.Session{}, err
		} else if fresh {
			return domain.Session{}, domain.ErrStoreUnavailable
		}
		return s.store.GetSession(ctx, owner, id)
	}
	grant, err := s.workspaceGrant(ctx, owner, session.ProjectID)
	if err != nil {
		return domain.Session{}, err
	}
	if err := s.engine.Available(ctx); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	count, err := s.store.CountActive(ctx, owner)
	if err != nil {
		return domain.Session{}, err
	}
	if session.State.Terminal() && count >= domain.MaxSessions {
		return domain.Session{}, domain.ErrSessionLimit
	}
	var generation int64
	err = s.AroundControl(ctx, id, func() error {
		if err := s.reapExact(ctx, session); err != nil {
			return err
		}
		var fresh bool
		var err error
		generation, fresh, err = restarts.BeginRestart(ctx, owner, id, key, time.Now().UTC(), mode)
		if err != nil {
			return err
		}
		if !fresh {
			return domain.ErrStoreUnavailable
		}
		return nil
	})
	if err != nil {
		return domain.Session{}, err
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		current, err := s.store.GetSession(cleanup, owner, id)
		if err != nil || current.Generation != generation {
			return
		}
		if err := s.reapExact(cleanup, current); err != nil {
			s.logger.Error("resident restart cleanup failed", "session_id", id, "error", err)
			return // keep the queued row adoptable instead of lying about a live child
		}
		_ = s.store.CloseSession(cleanup, owner, id, domain.StateFailed, time.Now().UTC())
	}()
	if fence != nil {
		if err := fence(); err != nil {
			return domain.Session{}, err
		}
	}
	release, err := s.engine.Reserve()
	if err != nil {
		return domain.Session{}, err
	}
	session.Generation = generation
	session.LifecycleMode = mode
	session.State = domain.StateQueued
	session.ChildContainerID = ""
	session.ChildImageID = ""
	session.ChildGeneration = 0
	display, err := s.launchResident(ctx, session, grant)
	if err != nil {
		release()
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	if err := s.bindResidentChild(ctx, session, display); err != nil {
		if stopper, ok := display.(ports.ExactStopper); ok {
			_ = stopper.StopExact(ctx)
		} else {
			display.Stop()
		}
		release()
		return domain.Session{}, err
	}
	s.mu.Lock()
	s.displays[id] = display
	s.releases[id] = release
	s.mu.Unlock()
	if err := s.store.UpdateState(ctx, owner, id, domain.StateRunning, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	result, err := s.store.GetSession(ctx, owner, id)
	if err != nil || result.Generation != generation {
		if err != nil {
			return domain.Session{}, err
		}
		return domain.Session{}, domain.ErrStoreUnavailable
	}
	completed = true
	s.watchWorkspace(result, grant)
	return result, nil
}

// stopResident checks the action receipt before touching Docker. A delayed
// replay must never reap a child started by a later Restart action.
func (s *Service) stopResident(ctx context.Context, owner, id, key string, fence func() error, stops ports.StopStore) (domain.Session, error) {
	store, ok := s.store.(ports.ResidentStore)
	if !ok {
		return domain.Session{}, domain.ErrStoreUnavailable
	}
	if found, err := store.StopReceipt(ctx, owner, id, key); err != nil {
		return domain.Session{}, err
	} else if found {
		return s.store.GetSession(ctx, owner, id)
	}
	session, err := s.store.GetSession(ctx, owner, id)
	if err != nil {
		return domain.Session{}, err
	}
	err = s.AroundControl(ctx, id, func() error {
		if err := s.reapExact(ctx, session); err != nil {
			return err
		}
		fresh, err := stops.BeginStop(ctx, owner, id, key, time.Now().UTC())
		if err != nil {
			return err
		}
		if !fresh {
			return domain.ErrStoreUnavailable
		}
		return nil
	})
	if err != nil {
		return domain.Session{}, err
	}
	if fence != nil {
		if err := fence(); err != nil {
			return domain.Session{}, err
		}
	}
	return s.store.GetSession(ctx, owner, id)
}
