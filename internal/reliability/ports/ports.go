// Package ports defines the Reliability module's neutral observation and
// control ports plus its own repository boundary. The runtime-host
// SupervisedWorkloadService satisfies the observer/control ports through a
// Connect client adapter; incidents persist in reliability-owned tables.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/reliability/domain"
)

// WorkloadState mirrors the runtime's supervised workload states as a plain
// bounded string.
type WorkloadState string

const (
	StatePending  WorkloadState = "pending"
	StateStarting WorkloadState = "starting"
	StateRunning  WorkloadState = "running"
	StateStopping WorkloadState = "stopping"
	StateStopped  WorkloadState = "stopped"
	StateFailed   WorkloadState = "failed"
	StateUnknown  WorkloadState = "unknown"
)

// Observation is the neutral, bounded observation fact the supervisor
// consumes. It carries identity, verdicts, and counters only.
type Observation struct {
	WorkloadID     string
	OwnerUserID    string
	ProjectID      string
	AppInstanceID  string
	AppID          string
	ManifestDigest string
	Generation     int64
	State          WorkloadState
	RestartCount   int64
	HealthVerdict  string // unknown | ok | failing
	ExitCategory   string // none | exited | oom | pids | unknown
	Idle           bool
	MemoryOOMs     uint64
	PIDsPeak       uint64
	ObservedAt     time.Time
}

// ControlOutcome is the sanitized control verdict replayed from the runtime.
type ControlOutcome string

const (
	ControlRestarted      ControlOutcome = "restarted"
	ControlStopped        ControlOutcome = "stopped"
	ControlLimitExhausted ControlOutcome = "limit_exhausted"
	ControlConflict       ControlOutcome = "conflict"
	ControlUnsupported    ControlOutcome = "unsupported"
	ControlUnavailable    ControlOutcome = "unavailable"
	ControlFailed         ControlOutcome = "failed"
)

// ControlResult pairs the outcome with the new generation when one exists.
type ControlResult struct {
	Outcome    ControlOutcome
	Generation int64
}

var (
	// ErrRuntimeUnavailable marks a temporarily unreachable runtime host.
	ErrRuntimeUnavailable = errors.New("runtime observation is temporarily unavailable")
)

// WorkloadObserver reads the runtime's neutral observation snapshot.
type WorkloadObserver interface {
	ListObservations(ctx context.Context) ([]Observation, error)
}

// WorkloadController applies bounded, idempotent control actions. The
// actionKey is the caller's durable idempotency key: same key replays the
// same verdict across both sides' restarts.
type WorkloadController interface {
	Restart(ctx context.Context, workloadID, actionKey string) (ControlResult, error)
	Stop(ctx context.Context, workloadID, actionKey, reason string) (ControlResult, error)
}

// StoredAction is the persisted control attempt of one incident decision.
type StoredAction struct {
	IncidentID       string
	Action           string // restart | terminate
	Outcome          ControlOutcome
	ResultGeneration int64
}

// IncidentFilter scopes the owner-facing list.
type IncidentFilter struct {
	OwnerUserID string
	ProjectID   string
	PageSize    int
	PageToken   string
}

// IncidentRepository owns the reliability facts: incidents, the per-incident
// action ledger, supervision progress, and the poll checkpoint.
type IncidentRepository interface {
	// CreateIncident inserts the incident; an existing occurrence digest
	// returns created=false with the stored row.
	CreateIncident(ctx context.Context, incident domain.Incident) (created bool, err error)
	// GetIncident returns one incident by ID.
	GetIncident(ctx context.Context, incidentID string) (domain.Incident, error)
	// ListIncidents returns one owner-scoped, bounded page. The token is the
	// last row's (created_at, id) pair; limit is the fetch size.
	ListIncidents(ctx context.Context, filter IncidentFilter, limit int) ([]domain.Incident, error)
	// UpdateOutcome applies a decision outcome to an open incident
	// (mitigated/failed transitions with the bounded restart outcome).
	UpdateOutcome(ctx context.Context, incidentID string, state domain.State, outcome domain.RestartOutcome, now time.Time) error
	// MarkResolved resolves a mitigated incident after the stable streak.
	MarkResolved(ctx context.Context, incidentID string, now time.Time) error
	// Acknowledge stamps the owner acknowledgement exactly once.
	Acknowledge(ctx context.Context, incidentID, ownerUserID string, now time.Time) error
	// ListOpenForWorkload returns the open/mitigated incidents of one
	// workload generation for decision bookkeeping.
	ListOpenForWorkload(ctx context.Context, workloadID string, generation int64) ([]domain.Incident, error)

	// RecordAction stores or updates the action ledger row.
	RecordAction(ctx context.Context, incidentID, action string, result ControlResult, now time.Time) error
	// LookupAction returns the stored action row, if any.
	LookupAction(ctx context.Context, incidentID, action string) (StoredAction, error)
	// ListPendingActionIncidents returns incidents whose decision has not
	// recorded a final action outcome (crash recovery for the caller).
	ListPendingActionIncidents(ctx context.Context, limit int) ([]domain.Incident, error)

	// LoadProgress returns the supervision progress of one workload, if any.
	LoadProgress(ctx context.Context, workloadID string) (WorkloadProgress, error)
	// SaveProgress persists the supervision progress of one workload.
	SaveProgress(ctx context.Context, progress WorkloadProgress, now time.Time) error

	// LoadCheckpoint returns the poll checkpoint timestamp.
	LoadCheckpoint(ctx context.Context) (time.Time, bool, error)
	// SaveCheckpoint persists the poll checkpoint.
	SaveCheckpoint(ctx context.Context, at time.Time) error
}

// WorkloadProgress is the supervision progress of one workload: the last
// observed facts, the stable streak, and the occurrence ordinals.
type WorkloadProgress struct {
	WorkloadID       string
	Generation       int64
	LastState        WorkloadState
	LastHealth       string
	LastExit         string
	LastRestart      int64
	StablePolls      int64
	ExitOccurrence   int64
	HealthOccurrence int64
	OOMOccurrence    int64
	PIDsOccurrence   int64
	FirstSeenAt      time.Time
}
