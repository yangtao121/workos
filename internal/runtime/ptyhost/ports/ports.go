// Package ports defines the supervised PTY store and engine boundaries
// (ADR-0028).
package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
)

// SessionStore owns the durable pty session rows (migration 055).
type SessionStore interface {
	InsertSession(ctx context.Context, session domain.Session) (storedDigest string, created bool, err error)
	GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error)
	GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error)
	UpdateState(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error
	CloseSession(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error
	ExpireIdle(ctx context.Context, now time.Time) ([]string, error)
	CountActive(ctx context.Context, ownerUserID string) (int, error)
}

// Engine facts as enforced for the pty supervisor.
type EngineFacts struct {
	Engine           string   `json:"engine"`
	ProcessGroupKill bool     `json:"process_group_kill"`
	ParentDeathSig   bool     `json:"parent_death_signal"`
	EnforcedLimits   []string `json:"enforced_limits"`
}

// Terminal is one live pty child.
type Terminal interface {
	// Write forwards bounded raw input to the child.
	Write(ctx context.Context, input []byte) error
	// Read returns output strictly after the cursor plus the next cursor.
	Read(ctx context.Context, after int64, maxBytes int32) (cursor int64, output []byte, err error)
	// Resize adjusts the pty window.
	Resize(ctx context.Context, columns, rows int32) error
	// Exited reports whether the child has been reaped.
	Exited() bool
	// Stop kills the process group and closes the pty.
	Stop()
}

// Engine launches one supervised login shell per session.
type Engine interface {
	Facts() EngineFacts
	Available(ctx context.Context) error
	Reserve() (release func(), err error)
	Launch(ctx context.Context, columns, rows int32) (Terminal, error)
}
