// Package ports defines the virtual-display native runner's neutral store and
// engine boundaries (ADR-0029).
package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

// SessionStore owns the durable native session rows (migration 056).
type SessionStore interface {
	ListActive(ctx context.Context) ([]domain.Session, error)
	// InsertSession persists a queued session; a same-owner/key replay
	// returns the stored request digest for drift adjudication.
	InsertSession(ctx context.Context, session domain.Session) (storedDigest string, created bool, err error)
	GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error)
	// GetSessionByKey resolves the stored session for an owner/key replay.
	GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error)
	// UpdateState persists the live facts (state).
	UpdateState(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error
	// CloseSession terminal-updates a closed/failed session.
	CloseSession(ctx context.Context, ownerUserID, sessionID string, state domain.State, now time.Time) error
	// ExpireIdle moves sessions past their idle deadline to closed; the
	// caller then reaps their displays.
	ExpireIdle(ctx context.Context, now time.Time) ([]string, error)
	// CountActive returns the owner's non-terminal session count.
	CountActive(ctx context.Context, ownerUserID string) (int, error)
}

// EngineFacts as enforced for this runner.
type EngineFacts struct {
	Engine           string   `json:"engine"`
	ProcessGroupKill bool     `json:"process_group_kill"`
	ParentDeathSig   bool     `json:"parent_death_signal"`
	CgroupIsolated   bool     `json:"cgroup_isolated"`
	EnforcedLimits   []string `json:"enforced_limits"`
}

// Display is one live supervised virtual display with its WebRTC peer.
type Display interface {
	// Connect feeds the client's complete offer and returns the complete
	// answer (candidates included). Reconnecting supersedes the prior peer.
	Connect(ctx context.Context, offerSDP string) (string, error)
	// Exited reports whether the display children are gone.
	Exited() bool
	// Stop kills the process group and removes the private scratch tree.
	Stop()
}

// Engine launches and supervises one virtual display per session.
type Engine interface {
	Facts() EngineFacts
	Available(ctx context.Context) error
	Reserve() (release func(), err error)
	Launch(ctx context.Context, width, height int32) (Display, error)
}
