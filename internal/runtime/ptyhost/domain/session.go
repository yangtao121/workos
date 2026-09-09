// Package domain holds the supervised PTY session facts (ADR-0028).
package domain

import (
	"errors"
	"time"
)

type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateClosed  State = "closed"
	StateFailed  State = "failed"
)

func (s State) Terminal() bool { return s == StateClosed || s == StateFailed }

const (
	MaxSessions     = 4
	SessionTTL      = 30 * time.Minute
	MaxOutputBuffer = 256 * 1024
	MaxWriteBytes   = 16 * 1024
	MaxReadBytes    = 64 * 1024
	MinColumns      = 20
	MaxColumns      = 500
	MinRows         = 5
	MaxRows         = 200
)

var (
	ErrInvalid           = errors.New("pty session request is invalid")
	ErrNotFound          = errors.New("pty session not found")
	ErrIdempotencyDrift  = errors.New("pty session replay request drifted")
	ErrSessionLimit      = errors.New("pty session limit reached")
	ErrEngineUnavailable = errors.New("pty engine is unavailable")
	ErrStoreUnavailable  = errors.New("pty store is temporarily unavailable")
)

// Session is the durable pty session row.
type Session struct {
	SessionID      string
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
	RequestDigest  string
	State          State
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
}

// ValidUUIDv7 matches the canonical resource id grammar (lowercase).
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
	return value[14] == '7' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}

// ValidSize bounds the initial terminal window.
func ValidSize(columns, rows int32) bool {
	return columns >= MinColumns && columns <= MaxColumns && rows >= MinRows && rows <= MaxRows
}

// ValidInput bounds raw terminal input: control sequences are the point, so
// only NUL is rejected alongside the byte budget.
func ValidInput(input []byte) bool {
	for _, b := range input {
		if b == 0 {
			return false
		}
	}
	return len(input) <= MaxWriteBytes
}
