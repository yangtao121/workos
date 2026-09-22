package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// Continuous harness session facts (ADR-0030). Core owns the association,
// authorization, and input ordering; the native context stays in the harness.
var (
	ErrSessionNotFound      = errors.New("agent session not found")
	ErrSessionClosed        = errors.New("agent session is closed")
	ErrSessionBusy          = errors.New("agent session already has an active execution")
	ErrSessionInputConflict = errors.New("session input key was used for a different request")
	ErrSessionInputInvalid  = errors.New("invalid agent session input")
)

type SessionState string

const (
	SessionStateNeedsReview SessionState = "needs_review"
	SessionStateActive      SessionState = "active"
	SessionStateClosing     SessionState = "closing"
	SessionStateClosed      SessionState = "closed"
)

func (s SessionState) Terminal() bool {
	return s == SessionStateClosed
}

// SessionInputState orders one input through the session lifecycle. The
// harness never sees an input in a terminal state; dispatch binds it to a
// Task run whose own state machine drives the terminal transition.
type SessionInputState string

const (
	SessionInputAccepted   SessionInputState = "accepted"
	SessionInputDispatched SessionInputState = "dispatched"
	SessionInputCompleted  SessionInputState = "completed"
	SessionInputFailed     SessionInputState = "failed"
	SessionInputCancelled  SessionInputState = "cancelled"
)

func (s SessionInputState) Terminal() bool {
	return s == SessionInputCompleted || s == SessionInputFailed || s == SessionInputCancelled
}

type Session struct {
	ID                       string
	OwnerUserID              string
	ProjectID                string
	IdempotencyKey           string
	WorkspaceBindingID       string
	WorkspaceBindingRevision int64
	ProviderID               string
	ProfileID                string
	State                    SessionState
	NativeSessionRef         string
	ActiveTaskID             string
	InputSequence            int64
	EventSequence            int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
	ClosedAt                 *time.Time
	Goal                     *GoalProjection
	GoalPauseRef             string
	Delegations              []Delegation
}

const (
	maximumSessionInputBytes  = 64 * 1024
	maximumResultSummaryBytes = 2048
)

// AcceptSessionInput validates and admits one input on an open session,
// assigning the next strictly increasing sequence. The bool reports whether
// the input must queue behind the active execution.
func (s *Session) AcceptSessionInput(now time.Time, inputID, clientInputID, text string) (SessionInput, bool, error) {
	return s.acceptSessionInput(now, inputID, clientInputID, text, nil)
}

func (s *Session) AcceptSessionDirective(now time.Time, inputID, clientInputID string, directive SessionDirective) (SessionInput, bool, error) {
	if err := directive.Validate(); err != nil {
		return SessionInput{}, false, err
	}
	return s.acceptSessionInput(now, inputID, clientInputID, "", &directive)
}

func (s *Session) acceptSessionInput(now time.Time, inputID, clientInputID, text string, directive *SessionDirective) (SessionInput, bool, error) {
	if s.State != SessionStateActive {
		return SessionInput{}, false, ErrSessionClosed
	}
	if directive == nil && (!utf8.ValidString(text) || utf8.RuneCountInString(text) == 0 || len(text) > maximumSessionInputBytes) {
		return SessionInput{}, false, ErrSessionInputInvalid
	}
	queued := s.ActiveTaskID != ""
	digest := InputRequestDigest(clientInputID, text)
	if directive != nil {
		digest = DirectiveRequestDigest(clientInputID, *directive)
	}
	s.InputSequence++
	return SessionInput{
		ID:            inputID,
		SessionID:     s.ID,
		OwnerUserID:   s.OwnerUserID,
		ClientInputID: clientInputID,
		Text:          text,
		RequestDigest: digest,
		Directive:     directive,
		State:         SessionInputAccepted,
		Sequence:      s.InputSequence,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, queued, nil
}

// DispatchSessionInput binds an accepted input to a task run. Exactly one
// input may be dispatched at a time: dispatching while another execution is
// active is a state violation even though SubmitSessionInput accepted the
// input as queued.
func (s *Session) DispatchSessionInput(input *SessionInput, now time.Time, taskID string) error {
	if s.State.Terminal() {
		return ErrSessionClosed
	}
	if input.State != SessionInputAccepted || input.SessionID != s.ID {
		return ErrSessionInputInvalid
	}
	if s.ActiveTaskID != "" {
		return ErrSessionBusy
	}
	input.State = SessionInputDispatched
	input.TaskID = taskID
	input.UpdatedAt = now
	s.ActiveTaskID = taskID
	s.UpdatedAt = now
	return nil
}

// FinishSessionInput records the terminal state of the input that owns the
// active execution and frees the session for the next queued input.
func (s *Session) FinishSessionInput(input *SessionInput, now time.Time, terminal SessionInputState, summary string) error {
	if input.State != SessionInputDispatched || input.TaskID == "" {
		return ErrSessionInputInvalid
	}
	if !terminal.Terminal() {
		return ErrSessionInputInvalid
	}
	if len(summary) > maximumResultSummaryBytes {
		summary = summary[:maximumResultSummaryBytes]
	}
	input.State = terminal
	input.ResultSummary = summary
	input.UpdatedAt = now
	if s.ActiveTaskID == input.TaskID {
		s.ActiveTaskID = ""
	}
	s.UpdatedAt = now
	return nil
}

// CloseSession forbids new inputs. An active execution keeps running to its
// terminal state; queued inputs are cancelled by the application layer.
func (s *Session) CloseSession(now time.Time) error {
	if s.State.Terminal() {
		return ErrSessionClosed
	}
	s.State = SessionStateClosed
	closed := now
	s.ClosedAt = &closed
	s.UpdatedAt = now
	return nil
}

type SessionInput struct {
	ID            string
	SessionID     string
	OwnerUserID   string
	ClientInputID string
	Text          string
	RequestDigest string
	State         SessionInputState
	TaskID        string
	Sequence      int64
	ResultSummary string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Directive     *SessionDirective
}

// SessionEvent is one row of the bounded session lifecycle log.
type SessionEvent struct {
	Sequence   int64
	SessionID  string
	EventType  string
	Payload    []byte
	OccurredAt time.Time
}

// InputRequestDigest binds an idempotency key to its exact content: the same
// key with different text is a conflict, not a replay.
func InputRequestDigest(clientInputID, text string) string {
	digest := sha256.Sum256([]byte(clientInputID + "\x00" + text))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func DirectiveRequestDigest(clientInputID string, directive SessionDirective) string {
	encoded, _ := json.Marshal(directive)
	digest := sha256.Sum256(append([]byte(clientInputID+"\x00native-directive\x00"), encoded...))
	return "sha256:" + hex.EncodeToString(digest[:])
}
