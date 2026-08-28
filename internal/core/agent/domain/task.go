package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid          = errors.New("invalid agent task")
	ErrNotFound         = errors.New("agent task not found")
	ErrLeaseLost        = errors.New("task execution lease is not active")
	ErrTerminal         = errors.New("agent task is already terminal")
	ErrProjectDenied    = errors.New("project is outside the current identity scope")
	ErrProviderMismatch = errors.New("run provider does not match task provider snapshot")
	// ErrIdempotencyConflict marks an App task client key that was already
	// consumed by a different canonical request (same owner + app instance +
	// client key, different digest). Transport maps it to a sanitized Aborted.
	ErrIdempotencyConflict = errors.New("app task idempotency key was used for a different request")
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

// App bridge bounded-input grammar. The bridge never exposes the wide
// AgentTaskInput to untrusted iframes: role and goal are the only app fields.
const (
	MaxAppTaskRoleRunes        = 64
	MinAppTaskGoalBytes        = 1
	MaxAppTaskGoalBytes        = 16 * 1024
	MaxAppClientIdempotencyKey = 128
)

// ValidAppClientIdempotencyKey enforces the bridge run key grammar at the
// application boundary: valid UTF-8, 1..128 runes, no C0/C1/NUL controls.
func ValidAppClientIdempotencyKey(value string) bool {
	return validBoundedText(value, MaxAppClientIdempotencyKey, true)
}

// ValidAppTaskRole enforces the optional bounded role (max 64 runes).
func ValidAppTaskRole(value string) bool {
	return validBoundedText(value, MaxAppTaskRoleRunes, false)
}

// ValidAppTaskGoal enforces the bounded non-empty goal (1..16 KiB UTF-8).
func ValidAppTaskGoal(value string) bool {
	if len(value) < MinAppTaskGoalBytes || len(value) > MaxAppTaskGoalBytes {
		return false
	}
	return validBoundedText(value, MaxAppTaskGoalBytes, true)
}

// ValidAppTaskUUID reports whether value is a canonical hyphenated UUID,
// guarding task and app instance identifiers at the boundary.
func ValidAppTaskUUID(value string) bool {
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
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

func validBoundedText(value string, maxRunes int, nonEmpty bool) bool {
	if !utf8.ValidString(value) {
		return false
	}
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > maxRunes {
			return false
		}
	}
	if nonEmpty && count == 0 {
		return false
	}
	return true
}

// AppTaskRequestDigest digests the canonical bounded App run request with an
// explicit format version. Only the fields the untrusted client controls are
// covered — project scope, owner, and provider snapshot are forced
// server-side and therefore not part of the client request identity. Same
// key + same digest replays the first task; same key + any other digest is
// a stable conflict verdict.
func AppTaskRequestDigest(role, goal string) string {
	canonical := struct {
		Goal    string `json:"goal"`
		Role    string `json:"role"`
		Version string `json:"version"`
	}{Goal: goal, Role: role, Version: "workos.agent-app-run.v1"}
	// encoding/json cannot fail on these constrained string fields; struct
	// fields in alphabetical order keep the encoding deterministic.
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
