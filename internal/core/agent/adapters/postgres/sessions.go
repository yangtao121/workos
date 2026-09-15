package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/yangtao121/workos/internal/core/agent/adapters/postgres/agentdb"
	"github.com/yangtao121/workos/internal/core/agent/domain"
)

// SessionRepository persists continuous harness session facts (ADR-0030):
// sessions, idempotent ordered inputs, and the bounded lifecycle event log.
type SessionRepository struct {
	queries *agentdb.Queries
}

func NewSessionRepository(queries *agentdb.Queries) *SessionRepository {
	return &SessionRepository{queries: queries}
}

func sessionError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w: %w", operation, domain.ErrSessionNotFound, err)
	}
	return err
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func sessionFromRow(row agentdb.WorkosCoreAgentSession) domain.Session {
	session := domain.Session{
		ID:                       row.SessionID,
		OwnerUserID:              row.OwnerUserID,
		ProjectID:                row.ProjectID,
		IdempotencyKey:           row.IdempotencyKey,
		WorkspaceBindingID:       uuidString(row.WorkspaceBindingID),
		WorkspaceBindingRevision: row.WorkspaceBindingRevision,
		ProviderID:               row.ProviderID,
		ProfileID:                row.ProfileID,
		State:                    domain.SessionState(row.State),
		NativeSessionRef:         row.NativeSessionRef,
		ActiveTaskID:             uuidString(row.ActiveTaskID),
		InputSequence:            row.InputSequence,
		EventSequence:            row.EventSequence,
		CreatedAt:                row.CreatedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
	if row.ClosedAt.Valid {
		closed := row.ClosedAt.Time
		session.ClosedAt = &closed
	}
	return session
}

func inputFromRow(row agentdb.WorkosCoreAgentSessionInput) domain.SessionInput {
	return domain.SessionInput{
		ID:            row.InputID,
		SessionID:     row.SessionID,
		OwnerUserID:   row.OwnerUserID,
		ClientInputID: row.ClientInputID,
		Text:          row.InputText,
		RequestDigest: row.RequestDigest,
		State:         domain.SessionInputState(row.State),
		TaskID:        uuidString(row.TaskID),
		Sequence:      row.Sequence,
		ResultSummary: row.ResultSummary,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}
}

// InsertSession creates the durable session row. The bool reports creation;
// a consumed key must be read back by the caller for replay adjudication.
func (r *SessionRepository) InsertSession(ctx context.Context, session domain.Session) (bool, error) {
	bindingID, err := nullableUUID(session.WorkspaceBindingID)
	if err != nil {
		return false, sessionError("insert agent session", err)
	}
	created, err := r.queries.InsertAgentSession(ctx, agentdb.InsertAgentSessionParams{
		SessionID: session.ID, OwnerUserID: session.OwnerUserID, ProjectID: session.ProjectID,
		IdempotencyKey: session.IdempotencyKey, WorkspaceBindingID: bindingID,
		WorkspaceBindingRevision: session.WorkspaceBindingRevision, ProviderID: session.ProviderID,
		ProfileID: session.ProfileID, NativeSessionRef: session.NativeSessionRef, CreatedAt: timestamp(session.CreatedAt),
	})
	if err != nil {
		return false, sessionError("insert agent session", err)
	}
	return created != 0, nil
}

func (r *SessionRepository) GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	row, err := r.queries.GetAgentSession(ctx, agentdb.GetAgentSessionParams{OwnerUserID: ownerUserID, SessionID: sessionID})
	if err != nil {
		return domain.Session{}, sessionError("get agent session", err)
	}
	return sessionFromRow(row), nil
}

func (r *SessionRepository) GetSessionByIdempotency(ctx context.Context, ownerUserID, key string) (domain.Session, error) {
	row, err := r.queries.GetAgentSessionByIdempotency(ctx, agentdb.GetAgentSessionByIdempotencyParams{OwnerUserID: ownerUserID, IdempotencyKey: key})
	if err != nil {
		return domain.Session{}, sessionError("get agent session by key", err)
	}
	return sessionFromRow(row), nil
}

func (r *SessionRepository) ListSessions(ctx context.Context, ownerUserID, projectID string, includeClosed bool, limit int) ([]domain.Session, error) {
	rows, err := r.queries.ListAgentSessions(ctx, agentdb.ListAgentSessionsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID, Column3: includeClosed, Limit: int32(limit),
	})
	if err != nil {
		return nil, sessionError("list agent sessions", err)
	}
	out := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, sessionFromRow(row))
	}
	return out, nil
}

// InsertInput persists one input with its sequence. The bool reports
// creation; a consumed (session, client_input_id) pair must be read back so
// the digest decides replay versus conflict.
func (r *SessionRepository) InsertInput(ctx context.Context, input domain.SessionInput) (bool, error) {
	created, err := r.queries.InsertAgentSessionInput(ctx, agentdb.InsertAgentSessionInputParams{
		InputID: input.ID, SessionID: input.SessionID, OwnerUserID: input.OwnerUserID,
		ClientInputID: input.ClientInputID, InputText: input.Text, RequestDigest: input.RequestDigest,
		Sequence: input.Sequence, CreatedAt: timestamp(input.CreatedAt),
	})
	if err != nil {
		return false, sessionError("insert agent session input", err)
	}
	return created != 0, nil
}

// BumpInputSequence advances the session's input counter under the exact
// prior value, making sequence assignment atomic.
func (r *SessionRepository) BumpInputSequence(ctx context.Context, ownerUserID, sessionID string, next, previous int64, now time.Time) (bool, error) {
	updated, err := r.queries.UpdateAgentSessionInputSequence(ctx, agentdb.UpdateAgentSessionInputSequenceParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, InputSequence: next, UpdatedAt: timestamp(now), InputSequence_2: previous,
	})
	if err != nil {
		return false, sessionError("bump input sequence", err)
	}
	return updated != 0, nil
}

func (r *SessionRepository) GetInput(ctx context.Context, sessionID, clientInputID string) (domain.SessionInput, error) {
	row, err := r.queries.GetAgentSessionInput(ctx, agentdb.GetAgentSessionInputParams{SessionID: sessionID, ClientInputID: clientInputID})
	if err != nil {
		return domain.SessionInput{}, sessionError("get agent session input", err)
	}
	return inputFromRow(row), nil
}

func (r *SessionRepository) ListInputs(ctx context.Context, sessionID string, afterSequence int64, limit int) ([]domain.SessionInput, error) {
	rows, err := r.queries.ListAgentSessionInputs(ctx, agentdb.ListAgentSessionInputsParams{
		SessionID: sessionID, Sequence: afterSequence, Limit: int32(limit),
	})
	if err != nil {
		return nil, sessionError("list agent session inputs", err)
	}
	out := make([]domain.SessionInput, 0, len(rows))
	for _, row := range rows {
		out = append(out, inputFromRow(row))
	}
	return out, nil
}

// ClaimExecution atomically binds the active task. The bool reports the
// claim; a concurrent claimant loses without side effects.
func (r *SessionRepository) ClaimExecution(ctx context.Context, ownerUserID, sessionID, taskID string, now time.Time) (bool, error) {
	claimedTask, err := requiredUUID(taskID)
	if err != nil {
		return false, sessionError("claim agent session execution", err)
	}
	claimed, err := r.queries.ClaimAgentSessionExecution(ctx, agentdb.ClaimAgentSessionExecutionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, ActiveTaskID: claimedTask, UpdatedAt: timestamp(now),
	})
	if err != nil {
		return false, sessionError("claim agent session execution", err)
	}
	return claimed != 0, nil
}

// ReleaseExecution frees the active execution only when it still points at
// the given task, so a stale worker cannot clobber a newer claim.
func (r *SessionRepository) ReleaseExecution(ctx context.Context, ownerUserID, sessionID, taskID string, now time.Time) error {
	releasedTask, err := requiredUUID(taskID)
	if err != nil {
		return sessionError("release agent session execution", err)
	}
	_, err = r.queries.ReleaseAgentSessionExecution(ctx, agentdb.ReleaseAgentSessionExecutionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, UpdatedAt: timestamp(now), Column4: releasedTask.String(),
	})
	return sessionError("release agent session execution", err)
}

// DispatchInput binds the accepted input to its task id.
func (r *SessionRepository) DispatchInput(ctx context.Context, inputID, taskID string, now time.Time) (bool, error) {
	dispatchedTask, err := requiredUUID(taskID)
	if err != nil {
		return false, sessionError("dispatch agent session input", err)
	}
	updated, err := r.queries.DispatchAgentSessionInput(ctx, agentdb.DispatchAgentSessionInputParams{
		InputID: inputID, Column2: dispatchedTask.String(), UpdatedAt: timestamp(now),
	})
	if err != nil {
		return false, sessionError("dispatch agent session input", err)
	}
	return updated != 0, nil
}

// FinishInput records the terminal state and bounded summary.
func (r *SessionRepository) FinishInput(ctx context.Context, inputID string, terminal domain.SessionInputState, summary string, now time.Time) (bool, error) {
	updated, err := r.queries.FinishAgentSessionInput(ctx, agentdb.FinishAgentSessionInputParams{
		InputID: inputID, Column2: string(terminal), Column3: summary, UpdatedAt: timestamp(now),
	})
	if err != nil {
		return false, sessionError("finish agent session input", err)
	}
	return updated != 0, nil
}

// CancelQueuedInputs terminal-cancels every accepted input of the session.
func (r *SessionRepository) CancelQueuedInputs(ctx context.Context, sessionID string, now time.Time) (int64, error) {
	cancelled, err := r.queries.CancelQueuedAgentSessionInputs(ctx, agentdb.CancelQueuedAgentSessionInputsParams{
		SessionID: sessionID, UpdatedAt: timestamp(now),
	})
	if err != nil {
		return 0, sessionError("cancel queued agent session inputs", err)
	}
	return cancelled, nil
}

func (r *SessionRepository) ListDispatchableInputs(ctx context.Context, sessionID string, limit int) ([]domain.SessionInput, error) {
	rows, err := r.queries.ListDispatchableAgentSessionInputs(ctx, agentdb.ListDispatchableAgentSessionInputsParams{
		SessionID: sessionID, Limit: int32(limit),
	})
	if err != nil {
		return nil, sessionError("list dispatchable agent session inputs", err)
	}
	out := make([]domain.SessionInput, 0, len(rows))
	for _, row := range rows {
		out = append(out, inputFromRow(row))
	}
	return out, nil
}

// AppendEvent persists one lifecycle event under the exact prior sequence.
func (r *SessionRepository) AppendEvent(ctx context.Context, sessionID string, sequence int64, eventType string, payload []byte, occurredAt time.Time) error {
	if _, err := r.queries.AppendAgentSessionEvent(ctx, agentdb.AppendAgentSessionEventParams{
		SessionID: sessionID, Sequence: sequence, EventType: eventType, Payload: payload, OccurredAt: timestamp(occurredAt),
	}); err != nil {
		return sessionError("append agent session event", err)
	}
	return nil
}

// BumpEventSequence advances the event counter under the exact prior value.
func (r *SessionRepository) BumpEventSequence(ctx context.Context, ownerUserID, sessionID string, next, previous int64, now time.Time) (bool, error) {
	updated, err := r.queries.BumpAgentSessionEventSequence(ctx, agentdb.BumpAgentSessionEventSequenceParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, EventSequence: next, UpdatedAt: timestamp(now), EventSequence_2: previous,
	})
	if err != nil {
		return false, sessionError("bump agent session event sequence", err)
	}
	return updated != 0, nil
}

// CloseSession terminal-closes the session row.
func (r *SessionRepository) CloseSession(ctx context.Context, ownerUserID, sessionID string, now time.Time) (bool, error) {
	closed, err := r.queries.CloseAgentSession(ctx, agentdb.CloseAgentSessionParams{
		OwnerUserID: ownerUserID, SessionID: sessionID, ClosedAt: timestamp(now),
	})
	if err != nil {
		return false, sessionError("close agent session", err)
	}
	return closed != 0, nil
}

// ListEvents pages the session lifecycle log for cursor-based catch-up.
func (r *SessionRepository) ListEvents(ctx context.Context, sessionID string, after int64, limit int) ([]domain.SessionEvent, error) {
	rows, err := r.queries.ListAgentSessionEvents(ctx, agentdb.ListAgentSessionEventsParams{
		SessionID: sessionID, Sequence: after, Limit: int32(limit),
	})
	if err != nil {
		return nil, sessionError("list agent session events", err)
	}
	out := make([]domain.SessionEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.SessionEvent{
			Sequence: row.Sequence, SessionID: row.SessionID, EventType: row.EventType,
			Payload: row.Payload, OccurredAt: row.OccurredAt.Time,
		})
	}
	return out, nil
}
