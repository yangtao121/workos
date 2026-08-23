package domain

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid       = errors.New("invalid agent task")
	ErrNotFound      = errors.New("agent task not found")
	ErrLeaseLost     = errors.New("task execution lease is not active")
	ErrTerminal      = errors.New("agent task is already terminal")
	ErrProjectDenied = errors.New("project is outside the current identity scope")
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateWaiting   State = "waiting"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

func (s State) Terminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

type Task struct {
	ID                    string
	OwnerUserID           string
	ProjectID             string
	Input                 json.RawMessage
	State                 State
	ProviderID            string
	HarnessInstanceID     string
	RunID                 string
	LastEventSequence     int64
	CancellationRequested bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Event struct {
	ID         string
	TaskID     string
	Sequence   int64
	EventType  string
	Payload    json.RawMessage
	OccurredAt time.Time
}

type Lease struct {
	ID        string
	WorkerID  string
	Task      Task
	ExpiresAt time.Time
}
