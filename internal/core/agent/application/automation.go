package application

import (
	"context"
	"errors"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
)

func validateSessionDirective(session domain.Session, directive domain.SessionDirective) error {
	if session.ActiveTaskID != "" {
		return domain.ErrSessionBusy
	}
	goal := session.Goal
	if directive.Kind == "create_goal" {
		if goal != nil && goal.Phase != "complete" {
			return domain.ErrSessionInputConflict
		}
		return nil
	}
	if goal == nil || goal.Ref != directive.GoalRef || goal.Revision != directive.ExpectedRevision || goal.Phase == "complete" {
		return domain.ErrSessionInputConflict
	}
	if directive.Kind == "resume_goal" && goal.RoundsStarted >= goal.MaxRounds {
		return domain.ErrSessionInputConflict
	}
	return nil
}

// RequestGoalPause records a human request without queueing behind a running
// goal. The native round driver observes it before the next model request.
// This acknowledges intent; the goal projection confirms the actual pause.
func (s *SessionService) RequestGoalPause(ctx context.Context, owner, id, key, ref string) (domain.Session, error) {
	if !validSessionOwner(owner) || !validSessionOwner(id) || key == "" || len(key) > 128 || ref == "" || len(ref) > 128 {
		return domain.Session{}, domain.ErrInvalid
	}
	err := s.withSession(ctx, owner, id, func(locked *SessionService) error {
		store, ok := locked.repository.(ports.SessionGoalPauseRepository)
		if !ok {
			return domain.ErrSessionInputInvalid
		}
		previous, err := store.GetGoalPauseRequest(ctx, id, key)
		if err == nil {
			if previous != ref {
				return domain.ErrSessionInputConflict
			}
			return nil
		}
		if !errors.Is(err, domain.ErrSessionNotFound) {
			return err
		}
		session, err := locked.repository.GetSession(ctx, owner, id)
		if err != nil {
			return err
		}
		if session.State != domain.SessionStateActive {
			return domain.ErrSessionClosed
		}
		if session.ActiveTaskID == "" || session.Goal == nil || session.Goal.Ref != ref || session.Goal.Phase != "active" {
			return domain.ErrSessionInputConflict
		}
		return store.RecordGoalPauseRequest(ctx, owner, id, key, ref, s.now().UTC())
	})
	if err != nil {
		return domain.Session{}, err
	}
	return s.repository.GetSession(ctx, owner, id)
}
