// Package domain holds the Remote Browser Pool session facts (ADR-0027).
package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type State string

const (
	StateQueued     State = "queued"
	StateRunning    State = "running"
	StateRestarting State = "restarting"
	StateClosed     State = "closed"
	StateFailed     State = "failed"
)

func (s State) Terminal() bool { return s == StateClosed || s == StateFailed }

const (
	MaxURLRunes   = 2048
	MaxSessions   = 4
	MaxRestarts   = 3
	SessionTTL    = 30 * time.Minute
	IdleTTL       = 5 * time.Minute
	MaxFrameBytes = 256 * 1024
	FrameWidth    = 1280
	FrameHeight   = 800
	FrameInterval = 500 * time.Millisecond
)

var (
	ErrInvalid           = errors.New("browser session request is invalid")
	ErrNotFound          = errors.New("browser session not found")
	ErrIdempotencyDrift  = errors.New("browser session replay request drifted")
	ErrSessionLimit      = errors.New("browser session limit reached")
	ErrEngineUnavailable = errors.New("browser engine is unavailable")
	// ErrStoreUnavailable marks a transient pool store outage.
	ErrStoreUnavailable = errors.New("browser pool store is temporarily unavailable")
)

// Session is the durable pool fact row.
type Session struct {
	SessionID      string
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
	RequestDigest  string
	State          State
	CurrentURL     string
	RestartCount   int32
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

// ValidURL enforces the only accepted scheme: http(s), bounded, no control
// characters. Empty is allowed (the session start page).
func ValidURL(raw string) bool {
	if raw == "" {
		return true
	}
	if !utf8.ValidString(raw) || utf8.RuneCountInString(raw) > MaxURLRunes {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return !strings.ContainsAny(raw, "\x00\r\n\t")
}
