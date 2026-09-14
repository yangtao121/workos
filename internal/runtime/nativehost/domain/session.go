// Package domain holds the supervised virtual-display native session facts
// (ADR-0029).
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
	MaxSessions     = 2
	SessionTTL      = 30 * time.Minute
	MinWidth        = 320
	MaxWidth        = 1920
	MinHeight       = 240
	MaxHeight       = 1200
	FrameRate       = 10
	MaxSDPBytes     = 64 * 1024
	MaxInputEvent   = 2 * 1024
	MaxTextRunes    = 256
	InputRatePerSec = 64
	InputBurst      = 128
)

var (
	ErrInvalid           = errors.New("native session request is invalid")
	ErrNotFound          = errors.New("native session not found")
	ErrIdempotencyDrift  = errors.New("native session replay request drifted")
	ErrSessionLimit      = errors.New("native session limit reached")
	ErrEngineUnavailable = errors.New("native engine is unavailable")
	ErrStoreUnavailable  = errors.New("native store is temporarily unavailable")
)

// Session is the durable native session row.
type Session struct {
	SessionID      string
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
	RequestDigest  string
	State          State
	Width          int32
	Height         int32
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

// ValidSize bounds the fixed initial display geometry.
func ValidSize(width, height int32) bool {
	return width >= MinWidth && width <= MaxWidth && height >= MinHeight && height <= MaxHeight
}
