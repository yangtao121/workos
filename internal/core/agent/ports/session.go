package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
)

// SessionTaskDispatcher admits one session input as a Task run through the
// same admission path as public SubmitTask: provider binding resolution,
// credential snapshot derivation, and budget policy all happen before any
// task row exists. Dispatch stamps the server-derived agent_session_id
// linkage onto the marshalled input (ADR-0030).
type SessionTaskDispatcher interface {
	Dispatch(ctx context.Context, ownerUserID, projectID, providerID, goal, idempotencyKey, sessionID string) (domain.Task, error)
	Cancel(ctx context.Context, ownerUserID, taskID, reason string) (domain.Task, error)
	Get(ctx context.Context, ownerUserID, taskID string) (domain.Task, error)
}

// SessionRepository persists continuous harness session facts (ADR-0030).
type SessionRepository interface {
	// WithinSession serializes mutations on a scoped session row. All repository
	// operations in apply commit together; external task admission remains idempotent.
	WithinSession(context.Context, string, string, func(SessionRepository) error) error
	RecoveryCandidates(context.Context, time.Time, int) ([]domain.Session, error)
	InputByTask(context.Context, string, string) (domain.SessionInput, error)
	PauseForReview(context.Context, string, string, time.Time) (bool, error)
	InsertSession(ctx context.Context, session domain.Session) (bool, error)
	GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error)
	GetSessionByIdempotency(ctx context.Context, ownerUserID, key string) (domain.Session, error)
	ListSessions(ctx context.Context, ownerUserID, projectID string, includeClosed bool, limit int) ([]domain.Session, error)
	InsertInput(ctx context.Context, input domain.SessionInput) (bool, error)
	BumpInputSequence(ctx context.Context, ownerUserID, sessionID string, next, previous int64, now time.Time) (bool, error)
	GetInput(ctx context.Context, sessionID, clientInputID string) (domain.SessionInput, error)
	ListInputs(ctx context.Context, sessionID string, afterSequence int64, limit int) ([]domain.SessionInput, error)
	ClaimExecution(ctx context.Context, ownerUserID, sessionID, taskID string, now time.Time) (bool, error)
	ReleaseExecution(ctx context.Context, ownerUserID, sessionID, taskID string, now time.Time) error
	DispatchInput(ctx context.Context, inputID, taskID string, now time.Time) (bool, error)
	FinishInput(ctx context.Context, inputID string, terminal domain.SessionInputState, summary string, now time.Time) (bool, error)
	CancelQueuedInputs(ctx context.Context, sessionID string, now time.Time) (int64, error)
	ListDispatchableInputs(ctx context.Context, sessionID string, limit int) ([]domain.SessionInput, error)
	AppendEvent(ctx context.Context, sessionID string, sequence int64, eventType string, payload []byte, occurredAt time.Time) error
	BumpEventSequence(ctx context.Context, ownerUserID, sessionID string, next, previous int64, now time.Time) (bool, error)
	CloseSession(ctx context.Context, ownerUserID, sessionID string, now time.Time) (bool, error)
	ListEvents(ctx context.Context, sessionID string, after int64, limit int) ([]domain.SessionEvent, error)
}
