package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

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
		if replay.ProjectID != projectID || replay.ProviderID != snapshot.ProviderID || replay.ProfileID != snapshot.ProfileID || replay.WorkspaceBindingID != snapshot.WorkspaceBindingID || replay.WorkspaceBindingRevision != snapshot.WorkspaceBindingRevision {
			return domain.Session{}, domain.ErrSessionInputConflict
		}
		return replay, nil
	}
	return s.repository.GetSession(ctx, ownerUserID, session.ID)
}

// Submit durably accepts one input. When the session is busy the input
// queues; when idle it dispatches immediately. Replays return the stored
// input; the same key with different text conflicts.
func (s *SessionService) Submit(ctx context.Context, ownerUserID, sessionID, clientInputID, text string) (domain.SessionInput, bool, error) {
	return s.submit(ctx, ownerUserID, sessionID, clientInputID, text, nil)
}

func (s *SessionService) SubmitDirective(ctx context.Context, ownerUserID, sessionID, clientInputID string, directive domain.SessionDirective) (domain.SessionInput, bool, error) {
	if err := directive.Validate(); err != nil {
		return domain.SessionInput{}, false, err
	}
	return s.submit(ctx, ownerUserID, sessionID, clientInputID, "", &directive)
}

func (s *SessionService) submit(ctx context.Context, ownerUserID, sessionID, clientInputID, text string, directive *domain.SessionDirective) (domain.SessionInput, bool, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) || clientInputID == "" || len(clientInputID) > 128 {
		return domain.SessionInput{}, false, domain.ErrInvalid
	}
	var input domain.SessionInput
	err := s.withSession(ctx, ownerUserID, sessionID, func(locked *SessionService) error {
		session, err := locked.repository.GetSession(ctx, ownerUserID, sessionID)
		if err != nil {
			return err
		}
		if session.State != domain.SessionStateActive {
			return domain.ErrSessionClosed
		}
		previous, err := locked.repository.GetInput(ctx, sessionID, clientInputID)
		if err == nil {
			digest := domain.InputRequestDigest(clientInputID, text)
			if directive != nil {
				digest = domain.DirectiveRequestDigest(clientInputID, *directive)
			}
			if previous.RequestDigest != digest {
				return domain.ErrSessionInputConflict
			}
			input = previous
			return nil
		}
		if !errors.Is(err, domain.ErrSessionNotFound) {
			return err
		}
		now := s.now().UTC()
		previousSequence := session.InputSequence
		var queued bool
		if directive == nil {
			input, queued, err = session.AcceptSessionInput(now, s.generator.New(), clientInputID, text)
		} else {
			if err := validateSessionDirective(session, *directive); err != nil {
				return err
			}
			input, queued, err = session.AcceptSessionDirective(now, s.generator.New(), clientInputID, *directive)
		}
		if err != nil {
			return err
		}
		if ok, err := locked.repository.BumpInputSequence(ctx, ownerUserID, sessionID, session.InputSequence, previousSequence, now); err != nil {
			return err
		} else if !ok {
			return domain.ErrSessionBusy
		}
		if ok, err := locked.repository.InsertInput(ctx, input); err != nil {
			return err
		} else if !ok {
			return domain.ErrSessionInputConflict
		}
		return locked.appendEvent(ctx, ownerUserID, sessionID, "input_accepted", map[string]any{"input_id": input.ID, "queued": queued}, now)
	})
	if err != nil {
		return domain.SessionInput{}, false, err
	}
	// Acceptance is already durable. A failed dispatch is repaired using the
	// same input identity, including a task whose admission acknowledgement was lost.
	if err := s.dispatchNext(ctx, ownerUserID, sessionID); err != nil {
		s.logger.Warn("session dispatch deferred", "session", sessionID)
	}
	input, err = s.repository.GetInput(ctx, sessionID, clientInputID)
	return input, input.State == domain.SessionInputAccepted, err
}

func (s *SessionService) withSession(ctx context.Context, owner, id string, fn func(*SessionService) error) error {
	return s.repository.WithinSession(ctx, owner, id, func(repository ports.SessionRepository) error {
		locked := *s
		locked.repository = repository
		return fn(&locked)
	})
}

// Admission may commit before this transaction, but the worker claim query
// cannot see that task until its session slot AND input linkage commit.
func (s *SessionService) dispatchNext(ctx context.Context, owner, id string) error {
	var session domain.Session
	var input domain.SessionInput
	err := s.withSession(ctx, owner, id, func(locked *SessionService) error {
		var err error
		session, err = locked.repository.GetSession(ctx, owner, id)
		if err != nil {
			return err
		}
		if session.State != domain.SessionStateActive || session.ActiveTaskID != "" {
			return nil
		}
		pending, err := locked.repository.ListDispatchableInputs(ctx, id, 1)
		if err != nil {
			return err
		}
		if len(pending) != 0 {
			input = pending[0]
		}
		return nil
	})
	if err != nil || input.ID == "" {
		return err
	}
	// Admission uses its own transaction. Never hold a session connection
	// while asking it for another: concurrent waiters can exhaust the pool.
	// Competing dispatchers use the same durable key; claim eligibility is
	// withheld until the second transaction binds the winning task below.
	key := fmt.Sprintf("session-%s-input-%s", id, input.ID)
	var task domain.Task
	if input.Directive != nil {
		dispatcher, ok := s.tasks.(ports.SessionDirectiveDispatcher)
		if !ok {
			return domain.ErrSessionInputInvalid
		}
		task, err = dispatcher.DispatchDirective(ctx, owner, session.ProjectID, session.ProviderID, key, id, *input.Directive)
	} else {
		task, err = s.tasks.Dispatch(ctx, owner, session.ProjectID, session.ProviderID, input.Text, key, id)
	}
	if err != nil {
		return err
	}
	cancelAdmitted := false
	err = s.withSession(ctx, owner, id, func(locked *SessionService) error {
		current, err := locked.repository.GetSession(ctx, owner, id)
		if err != nil {
			return err
		}
		saved, err := locked.repository.GetInput(ctx, id, input.ClientInputID)
		if err != nil {
			return err
		}
		if saved.TaskID == task.ID {
			return nil
		}
		if saved.State != domain.SessionInputAccepted || current.State != domain.SessionStateActive {
			cancelAdmitted = true
			return nil
		}
		if current.ActiveTaskID != "" {
			return domain.ErrSessionBusy
		}
		now := s.now().UTC()
		if ok, err := locked.repository.ClaimExecution(ctx, owner, id, task.ID, now); err != nil {
			return err
		} else if !ok {
			return domain.ErrSessionBusy
		}
		if ok, err := locked.repository.DispatchInput(ctx, input.ID, task.ID, now); err != nil {
			return err
		} else if !ok {
			return domain.ErrSessionInputInvalid
		}
		return locked.appendEvent(ctx, owner, id, "input_dispatched", map[string]any{"input_id": input.ID, "task_id": task.ID}, now)
	})
	if err == nil && cancelAdmitted {
		_, err = s.tasks.Cancel(ctx, owner, task.ID, "Session input cancelled before dispatch")
	}
	return err
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
	var active domain.SessionInput
	var cancelled int64
	err := s.withSession(ctx, ownerUserID, sessionID, func(locked *SessionService) error {
		session, err := locked.repository.GetSession(ctx, ownerUserID, sessionID)
		if err != nil {
			return err
		}
		cancelled, err = locked.repository.CancelQueuedInputs(ctx, sessionID, s.now().UTC())
		if err != nil {
			return err
		}
		if session.ActiveTaskID != "" {
			active, err = locked.repository.InputByTask(ctx, sessionID, session.ActiveTaskID)
		}
		return err
	})
	if err == nil && active.TaskID != "" {
		_, err = s.tasks.Cancel(ctx, ownerUserID, active.TaskID, reason)
	}
	return active, cancelled, err
}

// Close preserves the existing contract: the active execution can settle,
// while no further queued or new input may start.
func (s *SessionService) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !validSessionOwner(ownerUserID) || !validSessionOwner(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	err := s.withSession(ctx, ownerUserID, sessionID, func(locked *SessionService) error {
		session, err := locked.repository.GetSession(ctx, ownerUserID, sessionID)
		if err != nil {
			return err
		}
		if session.State.Terminal() {
			return nil
		}
		now := s.now().UTC()
		if _, err := locked.repository.CloseSession(ctx, ownerUserID, sessionID, now); err != nil {
			return err
		}
		if _, err := locked.repository.CancelQueuedInputs(ctx, sessionID, now); err != nil {
			return err
		}
		return locked.appendEvent(ctx, ownerUserID, sessionID, "state_changed", map[string]any{"previous": string(session.State), "current": "closed", "reason": "owner"}, now)
	})
	if err != nil {
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
	if !terminal.Terminal() {
		return domain.ErrSessionInputInvalid
	}
	if len(summary) > 2048 {
		summary = summary[:2048]
		for !utf8.ValidString(summary) {
			summary = summary[:len(summary)-1]
		}
	}
	err := s.withSession(ctx, ownerUserID, sessionID, func(locked *SessionService) error {
		input, err := locked.repository.InputByTask(ctx, sessionID, taskID)
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if input.State.Terminal() {
			return nil
		}
		now := s.now().UTC()
		if ok, err := locked.repository.FinishInput(ctx, input.ID, terminal, summary, now); err != nil {
			return err
		} else if !ok {
			return nil
		}
		if err := locked.repository.ReleaseExecution(ctx, ownerUserID, sessionID, taskID, now); err != nil {
			return err
		}
		if terminal != domain.SessionInputCompleted {
			paused, err := locked.repository.PauseForReview(ctx, ownerUserID, sessionID, now)
			if err != nil {
				return err
			}
			if paused {
				if err := locked.appendEvent(ctx, ownerUserID, sessionID, "state_changed", map[string]any{"previous": "active", "current": "needs_review", "reason": "Execution failed or was interrupted. Inspect workspace effects before starting a new session."}, now); err != nil {
					return err
				}
			}
		}
		return locked.appendEvent(ctx, ownerUserID, sessionID, "input_terminal", map[string]any{"input_id": input.ID, "task_id": taskID, "terminal_state": string(terminal), "result_summary": summary}, now)
	})
	if err != nil {
		return err
	}
	return s.dispatchNext(ctx, ownerUserID, sessionID)
}

// Reconcile rotates durably through sessions after admission or terminal
// acknowledgement loss. It never resubmits a dispatched input to the model.
func (s *SessionService) Reconcile(ctx context.Context) error {
	if recovery, ok := s.repository.(interface {
		CancelledAdmissions(context.Context) ([]domain.Task, error)
	}); ok {
		tasks, err := recovery.CancelledAdmissions(ctx)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if _, err := s.tasks.Cancel(ctx, task.OwnerUserID, task.ID, "Session input cancelled before dispatch"); err != nil {
				return err
			}
		}
	}

	sessions, err := s.repository.RecoveryCandidates(ctx, s.now().UTC(), 100)
	if err != nil {
		return err
	}
	for _, candidate := range sessions {
		session, err := s.repository.GetSession(ctx, candidate.OwnerUserID, candidate.ID)
		if err != nil {
			return err
		}
		if session.ActiveTaskID == "" {
			if err := s.dispatchNext(ctx, session.OwnerUserID, session.ID); err != nil {
				s.logger.Warn("session recovery deferred", "session", session.ID)
			}
			continue
		}
		task, err := s.tasks.Get(ctx, session.OwnerUserID, session.ActiveTaskID)
		if err != nil {
			return err
		}
		var terminal domain.SessionInputState
		switch task.State {
		case domain.StateCompleted:
			terminal = domain.SessionInputCompleted
		case domain.StateFailed:
			terminal = domain.SessionInputFailed
		case domain.StateCancelled:
			terminal = domain.SessionInputCancelled
		default:
			continue
		}
		if err := s.FinishTaskRun(ctx, session.OwnerUserID, session.ID, task.ID, terminal, ""); err != nil {
			return err
		}
	}
	return nil
}

// appendEvent is called only while the session row is locked. Event and
// counter commit together, so interruption cannot leave a permanent gap.
func (s *SessionService) appendEvent(ctx context.Context, owner, id, eventType string, payload map[string]any, now time.Time) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.ErrInvalid
	}
	session, err := s.repository.GetSession(ctx, owner, id)
	if err != nil {
		return err
	}
	next := session.EventSequence + 1
	if err := s.repository.AppendEvent(ctx, id, next, eventType, encoded, now); err != nil {
		return err
	}
	ok, err := s.repository.BumpEventSequence(ctx, owner, id, next, session.EventSequence, now)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrSessionBusy
	}
	return nil
}

func validSessionOwner(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 7 && parsed.String() == value
}
