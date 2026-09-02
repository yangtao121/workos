// Durable shadow-generation rebuild (ADR-0013 §9): requested → snapshotting →
// catching_up → validating → promoting → completed, with monotonic
// canceled/failed terminal states. Every phase checkpoint is persisted, a
// restart resumes from the durable facts, and the promotion is a
// single-row compare-and-swap so at most one generation ever becomes active.
package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

var (
	// ErrRebuildConflict marks a rebuild request whose idempotency key was
	// already used for a different canonical scope.
	ErrRebuildConflict = errors.New("rebuild idempotency key was already used for a different scope")
	// ErrRebuildLiveScope marks a scope that already has a live rebuild.
	ErrRebuildLiveScope = errors.New("a rebuild is already live for this scope")
	// ErrInvalidRebuild marks a malformed rebuild request.
	ErrInvalidRebuild = errors.New("rebuild request is invalid")
)

// RebuildScopeAll is the explicit whole-projection scope.
const RebuildScopeAll = "all"

// RebuildPhaseDwell bounds one state-machine pass so a single tick can never
// spin forever on one phase.
const maxSnapshotPagesPerPass = 50

// RebuildJobView is the safe operational projection of a rebuild job.
type RebuildJobView struct {
	ID               string
	Scope            string
	OwnerUserID      string
	ProjectID        string
	State            string
	PhaseCursor      string
	SnapshotBoundary string
	SourceCount      int64
	AppliedCount     int64
	TombstoneCount   int64
	FailureCategory  string
	TargetGeneration string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	TerminalAt       time.Time
}

// ValidateStoredRebuildJob revalidates the durable operational projection on
// every read. Database CHECK constraints are defense in depth, not a reason
// to trust a corrupted or manually restored row.
func ValidateStoredRebuildJob(job RebuildJobView) error {
	if !domain.ValidUUID(job.ID) || !domain.ValidUUID(job.TargetGeneration) ||
		job.SourceCount < 0 || job.AppliedCount < 0 || job.TombstoneCount < 0 ||
		job.AppliedCount < job.SourceCount || job.TombstoneCount > job.AppliedCount ||
		job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || len(job.PhaseCursor) > 4096 || len(job.SnapshotBoundary) > 4096 {
		return domain.ErrCorrupt
	}
	switch job.Scope {
	case RebuildScopeAll:
		if job.OwnerUserID != "" || job.ProjectID != "" {
			return domain.ErrCorrupt
		}
	case "project":
		if !domain.ValidUUID(job.OwnerUserID) || !domain.ValidUUID(job.ProjectID) {
			return domain.ErrCorrupt
		}
	default:
		return domain.ErrCorrupt
	}
	terminal := false
	switch job.State {
	case "requested", "snapshotting", "catching_up", "validating", "promoting":
	case "completed", "canceled", "failed":
		terminal = true
	default:
		return domain.ErrCorrupt
	}
	if terminal != !job.TerminalAt.IsZero() {
		return domain.ErrCorrupt
	}
	if job.UpdatedAt.Before(job.CreatedAt) || (!job.TerminalAt.IsZero() && job.TerminalAt.Before(job.CreatedAt)) {
		return domain.ErrCorrupt
	}
	return nil
}

// RebuildStore is the durable rebuild fact owner.
type RebuildStore interface {
	// AdjudicateRebuildRequest atomically creates generation + job + durable
	// request mapping, or replays the existing job for the same key/digest.
	AdjudicateRebuildRequest(ctx context.Context, key, digest string, job RebuildJobView) (RebuildJobView, bool, error)
	GetRebuildJob(ctx context.Context, jobID string) (RebuildJobView, error)
	LiveRebuildJobs(ctx context.Context) ([]RebuildJobView, error)
	SaveRebuildJob(ctx context.Context, job RebuildJobView) error
	CancelRebuildJob(ctx context.Context, jobID string, now time.Time) (bool, error)
	// ApplySnapshotSource projects one authoritative source into exactly the
	// target generation (never the active one) and records the receipt there.
	ApplySnapshotSource(ctx context.Context, effect SnapshotEffect, generation, requestDigest string, now time.Time) error
	// ValidateGeneration compares the target generation's document set
	// (digests, tombstones) against the authoritative map in the database.
	ValidateGeneration(ctx context.Context, generation string, authoritative map[string]string) (bool, error)
	// CompletePromotion atomically changes both generation statuses, swaps the
	// active pointer, and completes the job. A committed response loss replays
	// as completed instead of applying the transition twice.
	CompletePromotion(ctx context.Context, jobID, target, expectCurrent string, now time.Time) (bool, error)
	// FailRebuildJob atomically fails the target generation and job.
	FailRebuildJob(ctx context.Context, job RebuildJobView, category string, now time.Time) error
	// CleanupRetiredGeneration removes one retired generation's rebuildable
	// documents and receipts while retaining its bounded generation/job audit.
	CleanupRetiredGeneration(ctx context.Context) (bool, error)
}

// SnapshotSource is one authoritative fact the snapshot phase applies.
type SnapshotSource struct {
	OwnerUserID  string
	ProjectID    string
	ArtifactID   string
	ArtifactType string
	Digest       string
	CreatedAt    time.Time
}

// SnapshotApply is the content-resolved snapshot effect.
type SnapshotApply struct {
	Source  SnapshotSource
	Tombbed bool // authoritative page dropped it (archived project): tombstone
	Content []byte
	Title   string
	TaskID  string
}

// RebuildDriver carries the feed collaborators the machine needs. The live
// ingestion worker mirrors every publication into building generations, so
// catch-up is a Core-confirmed drain rather than a second consumer.
type RebuildDriver struct {
	feed CoreFeed
	now  func() time.Time
	ids  idsGenerator
}

// CoreFeed is the feed surface the rebuild consumes: the same private client
// the live worker uses, plus the projection's active-generation pointer for
// the promotion compare-and-swap.
type CoreFeed interface {
	ports.CoreFeedClient
	ActiveGenerationID(ctx context.Context) (string, error)
}

// NewRebuildDriver composes the driver (exported constructor keeps the
// fields encapsulated).
func NewRebuildDriver(feed CoreFeed, generator idsGenerator) RebuildDriver {
	return RebuildDriver{feed: feed, ids: generator}
}

// RebuildErrorCategory buckets terminal failures into safe categories.
func RebuildErrorCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrValidationMismatch):
		return "validation-mismatch"
	default:
		return "rebuild-pass-failed"
	}
}

// ErrValidationMismatch marks a target generation whose facts disagree with
// Core authority: it must never be promoted.
var ErrValidationMismatch = errors.New("rebuild validation found a mismatch against core authority")

// RebuildRequest is one adjudicated operator command.
type RebuildRequest struct {
	Scope          string // "all" | "project"
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
}

// RequestDigest binds the key to the canonical scope.
func (r RebuildRequest) RequestDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "workos.index.rebuild.v1\n%s\n%s\n%s", r.Scope, r.OwnerUserID, r.ProjectID)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// Validate checks the request grammar before any store work.
func (r RebuildRequest) Validate() error {
	switch r.Scope {
	case RebuildScopeAll:
		if r.OwnerUserID != "" || r.ProjectID != "" {
			return ErrInvalidRebuild
		}
	case "project":
		if !domain.ValidUUID(r.OwnerUserID) || !domain.ValidUUID(r.ProjectID) {
			return ErrInvalidRebuild
		}
	default:
		return ErrInvalidRebuild
	}
	if len(r.IdempotencyKey) == 0 || len(r.IdempotencyKey) > 128 || !utf8.ValidString(r.IdempotencyKey) {
		return ErrInvalidRebuild
	}
	for _, char := range r.IdempotencyKey {
		if (char >= 0 && char <= 0x1f) || (char >= 0x7f && char <= 0x9f) {
			return ErrInvalidRebuild
		}
	}
	return nil
}
