package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// SessionService owns the continuous harness session lifecycle (ADR-0030):
// sessions bind a project, workspace revision, and provider snapshot; inputs
// are idempotent and ordered; every dispatched input becomes one Task run
// through the existing submission path.
type SessionService struct {
	repository ports.SessionRepository
	tasks      ports.SessionTaskDispatcher
	generator  ids.Generator
	logger     *slog.Logger
	now        func() time.Time
}

func NewSessionService(repository ports.SessionRepository, tasks ports.SessionTaskDispatcher, generator ids.Generator, logger *slog.Logger) *SessionService {
	return &SessionService{repository: repository, tasks: tasks, generator: generator, logger: logger, now: time.Now}
}

// SessionSnapshot carries the provider and workspace facts a session pins at
// creation. The caller (transport/orchestration) derives them from the
// project binding; the session stores the immutable copy.
type SessionSnapshot struct {
	ProviderID               string
	ProfileID                string
	WorkspaceBindingID       string
	WorkspaceBindingRevision int64
	NativeSessionRef         string
}

// Create opens a session. Idempotent per key; replay returns the stored
// session.
func (s *SessionService) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey string, snapshot SessionSnapshot) (domain.Session, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || snapshot.ProviderID == "" || len(snapshot.ProviderID) > 64 || len(snapshot.ProfileID) > 128 {
		return domain.Session{}, domain.ErrInvalid
	}
	now := s.now().UTC()
	session := domain.Session{
		ID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		IdempotencyKey:     idempotencyKey,
		WorkspaceBindingID: snapshot.WorkspaceBindingID, WorkspaceBindingRevision: snapshot.WorkspaceBindingRevision,
		ProviderID: snapshot.ProviderID, ProfileID: snapshot.ProfileID,
		NativeSessionRef: snapshot.NativeSessionRef,
		State:            domain.SessionStateActive,
		CreatedAt:        now, UpdatedAt: now,
	}
	created, err := s.repository.InsertSession(ctx, session)
	if err != nil {
		return domain.Session{}, err
	}
	if !created {
		replay, err := s.repository.GetSessionByIdempotency(ctx, ownerUserID, idempotencyKey)
		if err != nil {
			return domain.Session{}, err
		}
		return replay, nil
	}
	if err := s.appendEvent(ctx, session.OwnerUserID, session.ID, "state_changed", map[string]any{"previous": "", "current": string(domain.SessionStateActive), "reason": "created"}, now); err != nil {
		return domain.Session{}, err
	}
	return s.repository.GetSession(ctx, ownerUserID, session.ID)
}

// Submit durably accepts one input. When the session is busy the input
// queues; when idle it dispatches immediately. Replays return the stored
// input; the same key with different text conflicts.
func (s *SessionService) Submit(ctx context.Context, ownerUserID, sessionID, clientInputID, text string) (domain.SessionInput, bool, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) || clientInputID == "" || len(clientInputID) > 128 {
		return domain.SessionInput{}, false, domain.ErrInvalid
	}
	session, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.SessionInput{}, false, err
	}
	if session.State.Terminal() {
		return domain.SessionInput{}, false, domain.ErrSessionClosed
	}
	now := s.now().UTC()
	// Reserve the next sequence atomically before persisting the input.
	next := session.InputSequence + 1
	bumped, err := s.repository.BumpInputSequence(ctx, ownerUserID, sessionID, next, session.InputSequence, now)
	if err != nil {
		return domain.SessionInput{}, false, err
	}
	if !bumped {
		return domain.SessionInput{}, false, domain.ErrSessionBusy
	}
	input, queued, err := session.AcceptSessionInput(now, s.generator.New(), clientInputID, text)
	if err != nil {
		return domain.SessionInput{}, false, err
	}
	created, err := s.repository.InsertInput(ctx, input)
	if err != nil {
		return domain.SessionInput{}, false, err
	}
	if !created {
		previous, readErr := s.repository.GetInput(ctx, sessionID, clientInputID)
		if readErr != nil {
			return domain.SessionInput{}, false, readErr
		}
		if previous.RequestDigest != input.RequestDigest {
			return domain.SessionInput{}, false, domain.ErrSessionInputConflict
		}
		return previous, previous.State != domain.SessionInputAccepted || previous.TaskID != "", nil
	}
	if err := s.appendEvent(ctx, session.OwnerUserID, session.ID, "input_accepted", map[string]any{"input_id": input.ID, "queued": queued}, now); err != nil {
		return domain.SessionInput{}, false, err
	}
	if queued {
		return input, true, nil
	}
	dispatched, err := s.dispatch(ctx, session, input, now)
	if err != nil {
		return input, true, err
	}
	return dispatched, false, nil
}

// dispatch claims the single execution slot, submits the Task, and records
// the linkage. A lost claim or failed submission leaves the input accepted
// so the sweeper can retry it in order.
func (s *SessionService) dispatch(ctx context.Context, session domain.Session, input domain.SessionInput, now time.Time) (domain.SessionInput, error) {
	task, err := s.tasks.Dispatch(ctx, session.OwnerUserID, session.ProjectID, session.ProviderID, input.Text, fmt.Sprintf("session-%s-input-%s", session.ID, input.ID), session.ID)
	if err != nil {
		return input, err
	}
	claimed, err := s.repository.ClaimExecution(ctx, session.OwnerUserID, session.ID, task.ID, now)
	if err != nil {
		return input, err
	}
	if !claimed {
		// Another input owns the slot; this input stays queued for the
		// sweeper. The submitted task is still a valid execution record;
		// it will be cancelled by the slot owner's terminal handling.
		if _, cancelErr := s.tasks.Cancel(ctx, session.OwnerUserID, task.ID, "session execution slot lost"); cancelErr != nil {
			s.logger.Warn("session slot-lost cancel failed", "task", task.ID, "error", cancelErr)
		}
		return input, domain.ErrSessionBusy
	}
	dispatched, err := s.repository.DispatchInput(ctx, input.ID, task.ID, now)
	if err != nil {
		return input, err
	}
	if !dispatched {
		return input, domain.ErrSessionInputInvalid
	}
	input.State = domain.SessionInputDispatched
	input.TaskID = task.ID
	if err := s.appendEvent(ctx, session.OwnerUserID, session.ID, "input_dispatched", map[string]any{"input_id": input.ID, "task_id": task.ID}, now); err != nil {
		return input, err
	}
	return input, nil
}

// GetInput is the timeout-safe read for a submitted key.
func (s *SessionService) GetInput(ctx context.Context, ownerUserID, sessionID, clientInputID string) (domain.SessionInput, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) || clientInputID == "" {
		return domain.SessionInput{}, domain.ErrInvalid
	}
	if _, err := s.repository.GetSession(ctx, ownerUserID, sessionID); err != nil {
		return domain.SessionInput{}, err
	}
	return s.repository.GetInput(ctx, sessionID, clientInputID)
}

func (s *SessionService) ListInputs(ctx context.Context, ownerUserID, sessionID string, afterSequence int64, limit int) ([]domain.SessionInput, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) || afterSequence < 0 || limit <= 0 || limit > 200 {
		return nil, domain.ErrInvalid
	}
	if _, err := s.repository.GetSession(ctx, ownerUserID, sessionID); err != nil {
		return nil, err
	}
	return s.repository.ListInputs(ctx, sessionID, afterSequence, limit)
}

func (s *SessionService) Get(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	return s.repository.GetSession(ctx, ownerUserID, sessionID)
}

func (s *SessionService) List(ctx context.Context, ownerUserID, projectID string, includeClosed bool) ([]domain.Session, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(projectID) {
		return nil, domain.ErrInvalid
	}
	return s.repository.ListSessions(ctx, ownerUserID, projectID, includeClosed, 100)
}

// CancelExecution cancels the active task and every queued input; the
// session stays open.
func (s *SessionService) CancelExecution(ctx context.Context, ownerUserID, sessionID, reason string) (domain.SessionInput, int64, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) {
		return domain.SessionInput{}, 0, domain.ErrInvalid
	}
	session, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.SessionInput{}, 0, err
	}
	now := s.now().UTC()
	cancelled, err := s.repository.CancelQueuedInputs(ctx, sessionID, now)
	if err != nil {
		return domain.SessionInput{}, 0, err
	}
	var active domain.SessionInput
	if session.ActiveTaskID != "" {
		if _, err := s.tasks.Cancel(ctx, ownerUserID, session.ActiveTaskID, reason); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return domain.SessionInput{}, cancelled, err
		}
		inputs, err := s.repository.ListInputs(ctx, sessionID, 0, 200)
		if err != nil {
			return domain.SessionInput{}, cancelled, err
		}
		for _, input := range inputs {
			if input.TaskID == session.ActiveTaskID && input.State == domain.SessionInputDispatched {
				active = input
				break
			}
		}
	}
	return active, cancelled, nil
}

// Close forbids new inputs. Active executions keep running; queued inputs
// are cancelled.
func (s *SessionService) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	now := s.now().UTC()
	if _, err := s.repository.CancelQueuedInputs(ctx, sessionID, now); err != nil {
		return domain.Session{}, err
	}
	closed, err := s.repository.CloseSession(ctx, ownerUserID, sessionID, now)
	if err != nil {
		return domain.Session{}, err
	}
	if !closed {
		if session.State.Terminal() {
			return domain.Session{}, domain.ErrSessionClosed
		}
		return domain.Session{}, domain.ErrInvalid
	}
	if err := s.appendEvent(ctx, session.OwnerUserID, session.ID, "state_changed", map[string]any{"previous": string(session.State), "current": string(domain.SessionStateClosed), "reason": "owner"}, now); err != nil {
		return domain.Session{}, err
	}
	return s.repository.GetSession(ctx, ownerUserID, sessionID)
}

// Events pages the lifecycle log for cursor-based catch-up.
func (s *SessionService) Events(ctx context.Context, ownerUserID, sessionID string, after int64, limit int) ([]domain.SessionEvent, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) || after < 0 || limit <= 0 || limit > 500 {
		return nil, domain.ErrInvalid
	}
	if _, err := s.repository.GetSession(ctx, ownerUserID, sessionID); err != nil {
		return nil, err
	}
	return s.repository.ListEvents(ctx, sessionID, after, limit)
}

// FinishTaskRun is called by the task terminal path (and the sweeper) to
// terminal-close the input owning the task and dispatch the next queued
// input in order.
func (s *SessionService) FinishTaskRun(ctx context.Context, ownerUserID, sessionID, taskID string, terminal domain.SessionInputState, summary string) error {
	session, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	inputs, err := s.repository.ListInputs(ctx, sessionID, 0, 200)
	if err != nil {
		return err
	}
	var finished domain.SessionInput
	for _, input := range inputs {
		if input.TaskID == taskID && input.State == domain.SessionInputDispatched {
			finished = input
			break
		}
	}
	if finished.ID == "" {
		return nil
	}
	if _, err := s.repository.FinishInput(ctx, finished.ID, terminal, summary, now); err != nil {
		return err
	}
	if err := s.repository.ReleaseExecution(ctx, ownerUserID, sessionID, taskID, now); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, session.OwnerUserID, session.ID, "input_terminal", map[string]any{"input_id": finished.ID, "task_id": taskID, "terminal_state": string(terminal), "result_summary": summary}, now); err != nil {
		return err
	}
	// Dispatch the next queued input, if any.
	fresh, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return err
	}
	if fresh.State.Terminal() || fresh.ActiveTaskID != "" {
		return nil
	}
	queued, err := s.repository.ListDispatchableInputs(ctx, sessionID, 1)
	if err != nil {
		return err
	}
	if len(queued) == 0 {
		return nil
	}
	if _, err := s.dispatch(ctx, fresh, queued[0], now); err != nil {
		s.logger.Warn("session queued dispatch failed", "session", sessionID, "input", queued[0].ID, "error", err)
	}
	return nil
}

// appendEvent appends one lifecycle event under optimistic sequence
// control. Concurrent writers re-read the session and retry, so callers may
// pass a stale snapshot.
func (s *SessionService) appendEvent(ctx context.Context, ownerUserID, sessionID string, eventType string, payload map[string]any, now time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.ErrInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		session, err := s.repository.GetSession(ctx, ownerUserID, sessionID)
		if err != nil {
			return err
		}
		next := session.EventSequence + 1
		if err := s.repository.AppendEvent(ctx, session.ID, next, eventType, encoded, now); err != nil {
			s.logger.Warn("session event append retry", "session", sessionID, "attempt", attempt, "next", next, "error", err)
			continue
		}
		bumped, err := s.repository.BumpEventSequence(ctx, ownerUserID, sessionID, next, session.EventSequence, now)
		if err != nil {
			return err
		}
		if bumped {
			return nil
		}
	}
	return domain.ErrSessionBusy
}

func validSessionOwner(value string) bool {
	return len(value) == 36 && value[8] == '-' && value[13] == '-' && value[18] == '-' && value[23] == '-'
}
