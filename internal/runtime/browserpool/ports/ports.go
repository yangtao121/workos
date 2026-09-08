// Package ports defines the Remote Browser Pool's neutral store and engine
// boundaries (ADR-0027).
package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/runtime/browserpool/domain"
)

// SessionStore owns the durable browser session rows (migration 054).
type SessionStore interface {
	// InsertSession persists a queued session; a same-owner/key replay
	// returns the stored request digest for drift adjudication.
	InsertSession(ctx context.Context, session domain.Session) (storedDigest string, created bool, err error)
	GetSession(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error)
	// GetSessionByKey resolves the stored session for an owner/key replay.
	GetSessionByKey(ctx context.Context, ownerUserID, idempotencyKey string) (domain.Session, error)
	// UpdateRunning persists the live worker facts (state/url/restarts).
	UpdateRunning(ctx context.Context, ownerUserID, sessionID string, state domain.State, currentURL string, restartCount int32, now time.Time) error
	// CloseSession terminal-updates a closed/failed session.
	CloseSession(ctx context.Context, ownerUserID, sessionID string, state domain.State, restartCount int32, now time.Time) error
	// ListActive returns non-terminal sessions oldest first, bounded.
	ListActive(ctx context.Context, limit int) ([]domain.Session, error)
	// ExpireIdle moves sessions without viewers past their idle deadline to
	// closed; the caller then reaps their workers.
	ExpireIdle(ctx context.Context, now time.Time) ([]string, error)
	// CountActive returns the owner's non-terminal session count.
	CountActive(ctx context.Context, ownerUserID string) (int, error)
}

// Engine facts as enforced for this pool.
type EngineFacts struct {
	Engine           string   `json:"engine"`
	ProcessGroupKill bool     `json:"process_group_kill"`
	ParentDeathSig   bool     `json:"parent_death_signal"`
	CgroupIsolated   bool     `json:"cgroup_isolated"`
	EnforcedLimits   []string `json:"enforced_limits"`
}

// Engine launches and supervises one real browser worker per session.
type Engine interface {
	Facts() EngineFacts
	Available(ctx context.Context) error
	Reserve() (release func(), err error)
}

// Worker is one live browser child.
type Worker interface {
	// Navigate drives the page to a fixed http(s) URL.
	Navigate(ctx context.Context, url string) error
	// Screenshot captures one bounded JPEG frame.
	Screenshot(ctx context.Context) ([]byte, error)
	// Exited reports whether the child has exited.
	Exited() bool
	// Stop kills the child and reaps its private directory.
	Stop()
}

// Launcher is the engine's worker factory: one real browser child plus its
// devtools endpoint URL.
type Launcher interface {
	LaunchWorker(ctx context.Context) (Worker, string, error)
}
