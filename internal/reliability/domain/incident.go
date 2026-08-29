// Package domain holds the Reliability module's invariants: the Incident
// fact, the violation vocabulary, and the occurrence-identity derivation
// that keeps at-least-once observation replays from double-reporting
// (ADR-0006 §6). Domain never imports database, Connect, HTTP, or the
// runtime module.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalid marks a request that violates the incident grammar.
	ErrInvalid = errors.New("incident request is invalid")
	// ErrNotFound marks an unknown or foreign incident.
	ErrNotFound = errors.New("incident is not available")
	// ErrIdempotencyConflict marks an acknowledge key reused for a different
	// incident.
	ErrIdempotencyConflict = errors.New("acknowledge key was used for a different incident")
	// ErrUnavailable marks a temporarily unreachable store.
	ErrUnavailable = errors.New("incident store is temporarily unavailable")
)

// Violation is the fixed incident vocabulary: mapped to summaries and
// severities, never carrying raw engine output.
type Violation string

const (
	ViolationUnexpectedExit Violation = "unexpected_exit"
	ViolationHealthFailure  Violation = "health_failure"
	ViolationOOM            Violation = "oom"
	ViolationPIDsLimit      Violation = "pids_limit"
	ViolationRestartLimit   Violation = "restart_limit_exhausted"
)

// Valid reports whether value is a known violation.
func (v Violation) Valid() bool {
	switch v {
	case ViolationUnexpectedExit, ViolationHealthFailure, ViolationOOM,
		ViolationPIDsLimit, ViolationRestartLimit:
		return true
	default:
		return false
	}
}

// Severity maps each violation to its fixed severity. A hard exit or OOM is
// critical; health and pids pressure are warnings; an exhausted restart
// budget is critical because the workload is now stopped.
func (v Violation) Severity() Severity {
	switch v {
	case ViolationUnexpectedExit, ViolationOOM, ViolationRestartLimit:
		return SeverityCritical
	default:
		return SeverityWarning
	}
}

// Summary is the fixed, content-free phrase for the violation. Raw engine
// output, HTTP bodies, logs, and user content never enter incident summaries.
func (v Violation) Summary() string {
	switch v {
	case ViolationUnexpectedExit:
		return "The app workload exited unexpectedly and was not restarted by the engine."
	case ViolationHealthFailure:
		return "The app workload stopped answering its health probe."
	case ViolationOOM:
		return "The app workload exceeded its memory limit."
	case ViolationPIDsLimit:
		return "The app workload hit its process limit."
	case ViolationRestartLimit:
		return "The app workload exhausted its restart budget and was stopped."
	default:
		return "The app workload reported a fault."
	}
}

// Severity is the fixed severity vocabulary.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// State is the incident lifecycle: open until a bounded action mitigates it,
// resolved after a bounded stable streak. Acknowledgement is an owner fact
// orthogonal to state.
type State string

const (
	StateOpen      State = "open"
	StateMitigated State = "mitigated"
	StateResolved  State = "resolved"
)

// RestartOutcome is the bounded action outcome of the incident's decision.
type RestartOutcome string

const (
	OutcomePending   RestartOutcome = "pending"
	OutcomeRestarted RestartOutcome = "restarted"
	OutcomeStopped   RestartOutcome = "stopped"
	OutcomeFailed    RestartOutcome = "failed"
)

// Incident is the durable incident fact.
type Incident struct {
	ID                 string
	OwnerUserID        string
	ProjectID          string
	AppInstanceID      string
	AppID              string
	WorkloadID         string
	WorkloadGeneration int64
	Violation          Violation
	Summary            string
	OccurrenceDigest   string
	EvidenceDigest     string
	State              State
	RestartOutcome     RestartOutcome
	Revision           int64
	AcknowledgedAt     *time.Time
	MitigatedAt        *time.Time
	ResolvedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// OccurrenceDigest derives the occurrence identity: the same (workload,
// generation, violation, occurrence ordinal) hashes to one digest, so a
// replayed observation can never create a second incident for one episode.
func OccurrenceDigest(workloadID string, generation int64, violation Violation, occurrence int64) string {
	canonical := fmt.Sprintf("workos.incident-occurrence.v1|%s|%d|%s|%d", workloadID, generation, violation, occurrence)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EvidenceDigest derives the bounded evidence identity of one observation
// snapshot: it proves what was seen without carrying the observation itself.
func EvidenceDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidUUIDv7 is the canonical identifier grammar shared with the rest of
// WorkOS: canonical lowercase hyphenated UUIDv7.
func ValidUUIDv7(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, c := range []byte(value) {
		switch index {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	if value[14] != '7' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// ValidIdempotencyKey enforces the acknowledge key grammar: valid UTF-8, no
// control characters, bounded length.
func ValidIdempotencyKey(value string) bool {
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > 128 {
			return false
		}
	}
	return count > 0
}

// Bounded observation verdict vocabularies (mirrored from the runtime's
// neutral observation contract; parsed by the runtime client adapter).
const (
	HealthUnknown = "unknown"
	HealthOK      = "ok"
	HealthFailing = "failing"
	ExitNone      = "none"
	ExitOOM       = "oom"
	ExitPIDs      = "pids"
)
